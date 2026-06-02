package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

const (
	WeatherStep       = 5 * time.Millisecond
	GridStep          = 100 * time.Millisecond
	WeatherPerGrid    = 12
	ForecastHorizon   = 5
	PredictorBufSize  = WeatherPerGrid
	MaxSimGridSteps   = 24
	BatteryCapacityMW = 100.0
)

type DemandReport struct {
	ID       string
	MW       float64
	Priority int
}

type SupplyStatus struct {
	AllocatedMW float64
	Reason      string
}

type ForecastReport struct {
	HorizonSteps int
	DeltaMW      float64
}

type WeatherData struct {
	WindSpeed float64
	Sunlight  float64
	Timestamp int64
}

type LogEntry struct {
	TimeStep int64
	Message  string
}

type EnergySource interface {
	Run(ctx context.Context)
	CurrentPower() float64
}

type Predictor interface {
	Run(ctx context.Context)
}

type Consumer interface {
	Run(ctx context.Context)
	DemandChan() chan<- DemandReport
	SupplyChan() <-chan SupplyStatus
}

type BatteryCommand struct {
	ChargeMW    float64
	DischargeMW float64
	GetSoC      bool
	Reply       chan float64
}

type EnergyStorage interface {
	Run(ctx context.Context)
	CmdChan() chan<- BatteryCommand
}

type WeatherProvider interface {
	Run(ctx context.Context)
}

type DataLogger interface {
	Run(ctx context.Context)
	Log(entry LogEntry)
}

type ConventionalPlant interface {
	Run(ctx context.Context)
	CurrentPower() float64
	Start()
	IsOn() bool
}

type WeatherStation struct {
	out chan<- WeatherData
}

func NewWeatherStation(out chan<- WeatherData) *WeatherStation {
	return &WeatherStation{out: out}
}

func (w *WeatherStation) Run(ctx context.Context) {
	ticker := time.NewTicker(WeatherStep)
	defer ticker.Stop()

	var wind float64 = 10
	var sun float64 = 50

	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			wind += rand.Float64()*2 - 1
			if wind < 0 {
				wind = 0
			}
			if wind > 30 {
				wind = 30
			}
			sun += rand.Float64()*4 - 2
			if sun < 0 {
				sun = 0
			}
			if sun > 100 {
				sun = 100
			}

			data := WeatherData{
				WindSpeed: wind,
				Sunlight:  sun,
				Timestamp: t.UnixNano(),
			}

			select {
			case w.out <- data:
			case <-ctx.Done():
				return
			}
		}
	}
}

type Broadcaster struct {
	in          <-chan WeatherData
	subscribers []chan WeatherData
}

func NewBroadcaster(in <-chan WeatherData, subs []chan WeatherData) *Broadcaster {
	return &Broadcaster{in: in, subscribers: subs}
}

func (b *Broadcaster) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-b.in:
			for _, ch := range b.subscribers {
				select {
				case ch <- data:
				default:
				}
			}
		}
	}
}

type WindFarm struct {
	weatherSub <-chan WeatherData
	mu         sync.RWMutex
	currentMW  float64
}

func NewWindFarm(weatherSub <-chan WeatherData) *WindFarm {
	return &WindFarm{weatherSub: weatherSub}
}

func (w *WindFarm) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-w.weatherSub:
			power := math.Min(200, data.WindSpeed*data.WindSpeed/10)
			w.mu.Lock()
			w.currentMW = power
			w.mu.Unlock()
		}
	}
}

func (w *WindFarm) CurrentPower() float64 {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.currentMW
}

type CoalPlant struct {
	mu        sync.Mutex
	state     string
	currentMW float64
	targetMW  float64
}

func NewCoalPlant(targetMW float64) *CoalPlant {
	return &CoalPlant{
		state:     "OFF",
		currentMW: 0,
		targetMW:  targetMW,
	}
}

func (c *CoalPlant) Start() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == "OFF" {
		c.state = "WARMING"
	}
}

func (c *CoalPlant) IsOn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == "ON"
}

func (c *CoalPlant) Run(ctx context.Context) {
	ticker := time.NewTicker(GridStep)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			switch c.state {
			case "OFF":
				c.currentMW = 0
			case "WARMING":
				c.currentMW += c.targetMW / 3
				if c.currentMW >= c.targetMW {
					c.currentMW = c.targetMW
					c.state = "ON"
				}
			case "ON":
				c.currentMW = c.targetMW
			}
			c.mu.Unlock()
		}
	}
}

func (c *CoalPlant) CurrentPower() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.currentMW
}

type SimplePredictor struct {
	weatherSub   <-chan WeatherData
	forecastChan chan<- ForecastReport
	logger       DataLogger

	buf []WeatherData
}

func NewSimplePredictor(sub <-chan WeatherData, forecastChan chan<- ForecastReport, logger DataLogger) *SimplePredictor {
	return &SimplePredictor{
		weatherSub:   sub,
		forecastChan: forecastChan,
		logger:       logger,
		buf:          make([]WeatherData, 0, PredictorBufSize),
	}
}

func (p *SimplePredictor) Run(ctx context.Context) {
	weatherTicker := time.NewTicker(WeatherStep)
	gridTicker := time.NewTicker(GridStep)
	defer weatherTicker.Stop()
	defer gridTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-weatherTicker.C:
			select {
			case data := <-p.weatherSub:
				if len(p.buf) >= PredictorBufSize {
					p.buf = p.buf[1:]
				}
				p.buf = append(p.buf, data)
			default:
			}
		case <-gridTicker.C:
			if len(p.buf) >= 2 {
				first := p.buf[0]
				last := p.buf[len(p.buf)-1]
				deltaWind := last.WindSpeed - first.WindSpeed
				deltaMW := deltaWind * 2
				fr := ForecastReport{
					HorizonSteps: ForecastHorizon,
					DeltaMW:      deltaMW,
				}
				select {
				case p.forecastChan <- fr:
				default:
				}
				p.logger.Log(LogEntry{
					TimeStep: time.Now().Unix(),
					Message:  fmt.Sprintf("Prognoza: zmiana mocy OZE o %.1f MW", deltaMW),
				})
			}
		}
	}
}

type Battery struct {
	cap float64
	soc float64
	cmd chan BatteryCommand
}

func NewBattery(capacityMW float64) *Battery {
	return &Battery{
		cap: capacityMW,
		soc: 0.5,
		cmd: make(chan BatteryCommand, 20),
	}
}

func (b *Battery) CmdChan() chan<- BatteryCommand {
	return b.cmd
}

func (b *Battery) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case cmd := <-b.cmd:
			if cmd.ChargeMW > 0 {
				maxEnergy := b.cap * (1 - b.soc)
				energy := cmd.ChargeMW
				if energy > maxEnergy {
					energy = maxEnergy
				}
				b.soc += energy / b.cap
				if b.soc > 1 {
					b.soc = 1
				}
			}

			if cmd.DischargeMW > 0 {
				maxEnergy := b.cap * b.soc
				energy := cmd.DischargeMW
				if energy > maxEnergy {
					energy = maxEnergy
				}
				b.soc -= energy / b.cap
				if b.soc < 0 {
					b.soc = 0
				}
				if cmd.Reply != nil {
					cmd.Reply <- energy
				}
			}

			if cmd.GetSoC {
				if cmd.Reply != nil {
					cmd.Reply <- b.soc
				}
			}
		}
	}
}

type BaseConsumer struct {
	id         string
	priority   int
	demandChan chan<- DemandReport
	supplyChan chan SupplyStatus
	logger     DataLogger
}

func NewBaseConsumer(id string, priority int, demandChan chan<- DemandReport, logger DataLogger) *BaseConsumer {
	return &BaseConsumer{
		id:         id,
		priority:   priority,
		demandChan: demandChan,
		supplyChan: make(chan SupplyStatus, 1),
		logger:     logger,
	}
}

func (c *BaseConsumer) DemandChan() chan<- DemandReport {
	return c.demandChan
}

func (c *BaseConsumer) SupplyChan() <-chan SupplyStatus {
	return c.supplyChan
}

func (c *BaseConsumer) Run(ctx context.Context) {
	ticker := time.NewTicker(GridStep)
	defer ticker.Stop()

	step := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			step++
			demand := c.profile(step)
			dr := DemandReport{
				ID:       c.id,
				MW:       demand,
				Priority: c.priority,
			}
			select {
			case c.demandChan <- dr:
			case <-ctx.Done():
				return
			}

			select {
			case status := <-c.supplyChan:
				if status.AllocatedMW < demand {
					c.logger.Log(LogEntry{
						TimeStep: int64(step),
						Message:  fmt.Sprintf("%s: dostał %.2f / %.2f (%s)", c.id, status.AllocatedMW, demand, status.Reason),
					})
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

func (c *BaseConsumer) profile(step int) float64 {
	hour := step % 24
	switch c.priority {
	case 3:
		if (hour >= 7 && hour <= 9) || (hour >= 18 && hour <= 22) {
			return 10
		}
		return 3
	case 2:
		if hour >= 6 && hour <= 18 {
			if hour%5 == 0 {
				return 40
			}
			return 30
		}
		return 5
	case 1:
		return 8
	default:
		return 5
	}
}

type GridHub struct {
	renewable EnergySource
	thermal   ConventionalPlant
	battery   EnergyStorage

	forecastChan <-chan ForecastReport
	demandChan   <-chan DemandReport
	weatherSub   <-chan WeatherData
	lastWeather  WeatherData
	logger       DataLogger

	consumersMu sync.Mutex
	consumers   map[string]*BaseConsumer

	lastDemandMu sync.Mutex
	lastDemand   map[string]DemandReport

	currentForecast ForecastReport
}

func NewGridHub(ren EnergySource, therm ConventionalPlant, batt EnergyStorage, forecast <-chan ForecastReport, demand <-chan DemandReport, weather <-chan WeatherData, logger DataLogger) *GridHub {
	return &GridHub{
		renewable: ren,
		thermal:   therm,
		battery:   batt,

		forecastChan: forecast,
		demandChan:   demand,
		weatherSub:   weather,
		logger:       logger,
		consumers:    make(map[string]*BaseConsumer),
		lastDemand:   make(map[string]DemandReport),
	}
}

func (g *GridHub) RegisterConsumer(c *BaseConsumer) {
	g.consumersMu.Lock()
	g.consumers[c.id] = c
	g.consumersMu.Unlock()
}

func (g *GridHub) Run(ctx context.Context) {
	ticker := time.NewTicker(GridStep)
	defer ticker.Stop()

	step := 0
	for {
		select {
		case <-ctx.Done():
			return

		case fr := <-g.forecastChan:
			g.currentForecast = fr
			if fr.DeltaMW < -10 {
				if !g.thermal.IsOn() {
					g.logger.Log(LogEntry{
						TimeStep: time.Now().Unix(),
						Message:  "Prognoza spadku OZE — uruchamiam elektrownię konwencjonalną",
					})
					g.thermal.Start()
				}
			}

		case dr := <-g.demandChan:
			g.lastDemandMu.Lock()
			g.lastDemand[dr.ID] = dr
			g.lastDemandMu.Unlock()
			g.logger.Log(LogEntry{
				TimeStep: time.Now().Unix(),
				Message:  fmt.Sprintf("%s chce %.1f MW (priorytet %d)", dr.ID, dr.MW, dr.Priority),
			})

		case wd := <-g.weatherSub:
			g.lastWeather = wd

		case <-ticker.C:
			step++
			g.balance(step)
		}
	}
}

func (g *GridHub) balance(step int) {
	type demandInfo struct {
		id       string
		priority int
		demand   float64
	}

	var demands []demandInfo

	g.consumersMu.Lock()
	for id, c := range g.consumers {
		_ = c
		g.lastDemandMu.Lock()
		dr, ok := g.lastDemand[id]
		g.lastDemandMu.Unlock()
		if !ok {
			continue
		}
		demands = append(demands, demandInfo{id: id, priority: dr.Priority, demand: dr.MW})
	}
	g.consumersMu.Unlock()

	totalDemand := 0.0
	for _, d := range demands {
		totalDemand += d.demand
	}

	renewProd := g.renewable.CurrentPower()
	thermProd := g.thermal.CurrentPower()
	production := renewProd + thermProd
	balance := production - totalDemand

	socReply := make(chan float64)
	g.battery.CmdChan() <- BatteryCommand{GetSoC: true, Reply: socReply}
	soc := <-socReply

	if balance > 0 {
		if soc < 1 {
			g.battery.CmdChan() <- BatteryCommand{ChargeMW: balance}
		} else {
			g.logger.Log(LogEntry{
				TimeStep: int64(step),
				Message:  fmt.Sprintf("Curtailment OZE: odrzucono nadwyżkę %.1f MW (SoC=100%%)", balance),
			})
			balance = 0
		}
	} else if balance < 0 {
		need := -balance
		reply := make(chan float64)
		g.battery.CmdChan() <- BatteryCommand{DischargeMW: need, Reply: reply}
		got := <-reply
		balance += got
	}

	state := "STABLE"
	if balance < 0 {
		state = "CRITICAL"
	}

	if balance < 0 {
		for i := 0; i < len(demands)-1; i++ {
			for j := i + 1; j < len(demands); j++ {
				if demands[i].priority < demands[j].priority {
					demands[i], demands[j] = demands[j], demands[i]
				}
			}
		}
		for _, d := range demands {
			if balance >= 0 {
				break
			}
			g.consumersMu.Lock()
			c := g.consumers[d.id]
			g.consumersMu.Unlock()
			if c == nil {
				continue
			}
			select {
			case c.supplyChan <- SupplyStatus{AllocatedMW: 0, Reason: "LoadShed"}:
			default:
			}
			balance += d.demand
			g.logger.Log(LogEntry{
				TimeStep: int64(step),
				Message:  fmt.Sprintf("Odłączono %s (priorytet %d, pobór %.1f MW)", d.id, d.priority, d.demand),
			})
		}
	}

	for _, d := range demands {
		g.consumersMu.Lock()
		c := g.consumers[d.id]
		g.consumersMu.Unlock()
		if c == nil {
			continue
		}
		alloc := d.demand
		if balance < 0 {
			alloc = math.Max(0, d.demand+balance/float64(len(demands)))
		}
		select {
		case c.supplyChan <- SupplyStatus{AllocatedMW: alloc, Reason: state}:
		default:
		}
	}

	socReply2 := make(chan float64)
	g.battery.CmdChan() <- BatteryCommand{GetSoC: true, Reply: socReply2}
	soc2 := <-socReply2

	g.logger.Log(LogEntry{
		TimeStep: int64(step),
		Message: fmt.Sprintf(
			"[Pogoda] Wiatr: %.1f km/h | Słońce: %.0f%%\n"+
				"[Produkcja] OZE: %.1f MW | Konwencjonalna: %.1f MW | Baterie: %.0f%% (SoC)\n"+
				"[Sieć] Popyt: %.1f MW | Bilans: %.1f MW | Stan: %s",
			g.lastWeather.WindSpeed,
			g.lastWeather.Sunlight,
			renewProd,
			thermProd,
			soc2*100,
			totalDemand,
			balance,
			state,
		),
	})
}

type CSVLogger struct {
	ch   chan LogEntry
	file *os.File
	w    *csv.Writer
	wg   *sync.WaitGroup
}

func NewCSVLogger(path string, wg *sync.WaitGroup) (*CSVLogger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"timestep", "message"})
	return &CSVLogger{
		ch:   make(chan LogEntry, 1000),
		file: f,
		w:    w,
		wg:   wg,
	}, nil
}

func (l *CSVLogger) Run(ctx context.Context) {
	l.wg.Add(1)
	defer l.wg.Done()

	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case e := <-l.ch:
					_ = l.w.Write([]string{fmt.Sprint(e.TimeStep), e.Message})
					fmt.Printf("[%d] %s\n", e.TimeStep, e.Message)
				default:
					l.w.Flush()
					l.file.Close()
					return
				}
			}
		case e := <-l.ch:
			_ = l.w.Write([]string{fmt.Sprint(e.TimeStep), e.Message})
			fmt.Printf("[%d] %s\n", e.TimeStep, e.Message)
		}
	}
}

func (l *CSVLogger) Log(entry LogEntry) {
	l.ch <- entry
}

func main() {
	rand.Seed(time.Now().UnixNano())
	weatherForGrid := make(chan WeatherData, 10)

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger, err := NewCSVLogger("logs/grid.csv", wg)
	if err != nil {
		fmt.Println("logger error:", err)
		return
	}

	weatherRaw := make(chan WeatherData, 10)
	weatherForFarm := make(chan WeatherData, 10)
	weatherForPredictor := make(chan WeatherData, 10)
	forecastChan := make(chan ForecastReport, 1)
	demandChan := make(chan DemandReport, 100)

	ws := NewWeatherStation(weatherRaw)
	bc := NewBroadcaster(weatherRaw, []chan WeatherData{
		weatherForFarm,
		weatherForPredictor,
		weatherForGrid,
	})

	wf := NewWindFarm(weatherForFarm)
	coal := NewCoalPlant(150)
	batt := NewBattery(BatteryCapacityMW)
	pred := NewSimplePredictor(weatherForPredictor, forecastChan, logger)
	grid := NewGridHub(wf, coal, batt, forecastChan, demandChan, weatherForGrid, logger)

	res := NewBaseConsumer("residential", 3, demandChan, logger)
	ind := NewBaseConsumer("industrial", 2, demandChan, logger)
	crit := NewBaseConsumer("critical", 1, demandChan, logger)

	grid.RegisterConsumer(res)
	grid.RegisterConsumer(ind)
	grid.RegisterConsumer(crit)

	go logger.Run(ctx)
	go ws.Run(ctx)
	go bc.Run(ctx)
	go wf.Run(ctx)
	go coal.Run(ctx)
	go batt.Run(ctx)
	go pred.Run(ctx)
	go grid.Run(ctx)
	go res.Run(ctx)
	go ind.Run(ctx)
	go crit.Run(ctx)

	<-sigCh
	fmt.Println("Zamykanie...")

	cancel()
	wg.Wait()
	fmt.Println("Zamknięto system.")
}
