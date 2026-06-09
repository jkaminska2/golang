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
	"sort"
	"sync"
	"syscall"
	"time"
)

const (
	WeatherStep      = 5 * time.Millisecond
	GridStep         = 100 * time.Millisecond
	WeatherPerGrid   = 12
	ForecastHorizon  = 5
	PredictorBufSize = WeatherPerGrid
	BatteryCapMW     = 100.0
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

type BatteryCommand struct {
	Kind  string
	MW    float64
	Reply chan<- BatteryResponse
}

type BatteryResponse struct {
	SoC     float64
	DelivMW float64
}

type PowerUpdate struct {
	SourceID string
	MW       float64
}

type ThermalStatus struct {
	IsOn bool
	MW   float64
}

type EnergySource interface {
	Run(ctx context.Context)
}

type Predictor interface {
	Run(ctx context.Context)
}

type Consumer interface {
	Run(ctx context.Context)
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
	StartChan() chan<- struct{}
}

type WeatherStation struct {
	out chan<- WeatherData
}

func NewWeatherStation(out chan<- WeatherData) *WeatherStation {
	return &WeatherStation{out: out}
}

func (ws *WeatherStation) Run(ctx context.Context) {
	ticker := time.NewTicker(WeatherStep)
	defer ticker.Stop()
	wind := 10.0
	sun := 50.0
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			wind += rand.Float64()*2 - 1
			wind = math.Max(0, math.Min(30, wind))
			sun += rand.Float64()*4 - 2
			sun = math.Max(0, math.Min(100, sun))
			data := WeatherData{WindSpeed: wind, Sunlight: sun, Timestamp: t.UnixNano()}
			select {
			case ws.out <- data:
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
	powerOut   chan<- PowerUpdate
}

func NewWindFarm(sub <-chan WeatherData, out chan<- PowerUpdate) *WindFarm {
	return &WindFarm{weatherSub: sub, powerOut: out}
}

func (wf *WindFarm) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-wf.weatherSub:
			power := math.Min(200, data.WindSpeed*data.WindSpeed/10)
			update := PowerUpdate{SourceID: "wind", MW: power}
			select {
			case wf.powerOut <- update:
			default:
			}
		}
	}
}

type CoalPlant struct {
	targetMW   float64
	startChan  chan struct{}
	thermalOut chan<- ThermalStatus
}

func NewCoalPlant(targetMW float64, out chan<- ThermalStatus) *CoalPlant {
	return &CoalPlant{
		targetMW:   targetMW,
		startChan:  make(chan struct{}, 1),
		thermalOut: out,
	}
}

func (c *CoalPlant) StartChan() chan<- struct{} {
	return c.startChan
}

func (c *CoalPlant) Run(ctx context.Context) {
	ticker := time.NewTicker(GridStep)
	defer ticker.Stop()

	state := "OFF"
	currentMW := 0.0

	push := func() {
		s := ThermalStatus{IsOn: state == "ON", MW: currentMW}
		select {
		case c.thermalOut <- s:
		default:
		}
	}

	for {
		select {
		case <-ctx.Done():
			return

		case <-c.startChan:
			if state == "OFF" {
				state = "WARMING"
				push()
			}

		case <-ticker.C:
			switch state {
			case "WARMING":
				currentMW += c.targetMW / 3
				if currentMW >= c.targetMW {
					currentMW = c.targetMW
					state = "ON"
				}
				push()
			case "ON":
				currentMW = c.targetMW
				push()
			}
		}
	}
}

type Battery struct {
	cap float64
	soc float64
	cmd chan BatteryCommand
}

func NewBattery(capMW float64) *Battery {
	return &Battery{cap: capMW, soc: 0.5, cmd: make(chan BatteryCommand, 30)}
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
			resp := BatteryResponse{SoC: b.soc}
			switch cmd.Kind {
			case "charge":
				space := b.cap * (1 - b.soc)
				energy := math.Min(cmd.MW, space)
				b.soc += energy / b.cap
				if b.soc > 1 {
					b.soc = 1
				}
				resp.DelivMW = energy
				resp.SoC = b.soc
			case "discharge":
				available := b.cap * b.soc
				energy := math.Min(cmd.MW, available)
				b.soc -= energy / b.cap
				if b.soc < 0 {
					b.soc = 0
				}
				resp.DelivMW = energy
				resp.SoC = b.soc
			case "get_soc":
				resp.SoC = b.soc
			}
			if cmd.Reply != nil {
				cmd.Reply <- resp
			}
		}
	}
}

type SimplePredictor struct {
	weatherSub   <-chan WeatherData
	forecastChan chan<- ForecastReport
	logger       DataLogger
	buf          []WeatherData
}

func NewSimplePredictor(sub <-chan WeatherData, fc chan<- ForecastReport, logger DataLogger) *SimplePredictor {
	return &SimplePredictor{
		weatherSub:   sub,
		forecastChan: fc,
		logger:       logger,
		buf:          make([]WeatherData, 0, PredictorBufSize),
	}
}

func (p *SimplePredictor) Run(ctx context.Context) {
	gridTicker := time.NewTicker(GridStep)
	defer gridTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case data := <-p.weatherSub:
			if len(p.buf) >= PredictorBufSize {
				p.buf = p.buf[1:]
			}
			p.buf = append(p.buf, data)
		case <-gridTicker.C:
			if len(p.buf) < 2 {
				continue
			}
			first := p.buf[0]
			last := p.buf[len(p.buf)-1]
			deltaMW := (last.WindSpeed - first.WindSpeed) * 2
			fr := ForecastReport{HorizonSteps: ForecastHorizon, DeltaMW: deltaMW}
			select {
			case p.forecastChan <- fr:
			default:
			}
			p.logger.Log(LogEntry{
				TimeStep: time.Now().Unix(),
				Message:  fmt.Sprintf("Predictor: zmiana mocy OZE o %.1f MW w ciągu %d kroków", deltaMW, ForecastHorizon),
			})
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

func NewBaseConsumer(id string, priority int, dc chan<- DemandReport, logger DataLogger) *BaseConsumer {
	return &BaseConsumer{
		id:         id,
		priority:   priority,
		demandChan: dc,
		supplyChan: make(chan SupplyStatus, 1),
		logger:     logger,
	}
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
			select {
			case c.demandChan <- DemandReport{ID: c.id, MW: demand, Priority: c.priority}:
			case <-ctx.Done():
				return
			}
			select {
			case status := <-c.supplyChan:
				if status.AllocatedMW < demand {
					c.logger.Log(LogEntry{
						TimeStep: int64(step),
						Message: fmt.Sprintf("%s: przydzielono %.2f / %.2f MW [%s]",
							c.id, status.AllocatedMW, demand, status.Reason),
					})
				}
			case <-time.After(GridStep * 2):
				c.logger.Log(LogEntry{
					TimeStep: int64(step),
					Message:  fmt.Sprintf("%s: brak odpowiedzi GridHub (timeout)", c.id),
				})
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

type consumerEntry struct {
	consumer   *BaseConsumer
	lastDemand DemandReport
}

type registerMsg struct {
	consumer *BaseConsumer
}

type GridHub struct {
	battery       EnergyStorage
	forecastChan  <-chan ForecastReport
	demandChan    <-chan DemandReport
	weatherSub    <-chan WeatherData
	windPowerChan <-chan PowerUpdate
	thermalChan   <-chan ThermalStatus
	startThermal  chan<- struct{}
	registerChan  chan registerMsg
	logger        DataLogger

	statsMu    sync.Mutex
	totalShed  float64
	totalSteps int
}

func NewGridHub(
	batt EnergyStorage,
	fc <-chan ForecastReport,
	dc <-chan DemandReport,
	ws <-chan WeatherData,
	windPower <-chan PowerUpdate,
	thermal <-chan ThermalStatus,
	startThermal chan<- struct{},
	logger DataLogger,
) *GridHub {
	return &GridHub{
		battery:       batt,
		forecastChan:  fc,
		demandChan:    dc,
		weatherSub:    ws,
		windPowerChan: windPower,
		thermalChan:   thermal,
		startThermal:  startThermal,
		registerChan:  make(chan registerMsg, 10),
		logger:        logger,
	}
}

func (g *GridHub) RegisterConsumer(c *BaseConsumer) {
	g.registerChan <- registerMsg{consumer: c}
}

func (g *GridHub) Run(ctx context.Context) {
	ticker := time.NewTicker(GridStep)
	defer ticker.Stop()

	consumers := make(map[string]*consumerEntry)
	pendingDemands := make(map[string]DemandReport)

	var lastWeather WeatherData
	var currentForecast ForecastReport

	renewMW := 0.0
	thermMW := 0.0
	thermOn := false

	step := 0

	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-g.registerChan:
			consumers[msg.consumer.id] = &consumerEntry{consumer: msg.consumer}
			g.logger.Log(LogEntry{
				TimeStep: time.Now().Unix(),
				Message:  fmt.Sprintf("Zarejestrowano: %s (priorytet %d)", msg.consumer.id, msg.consumer.priority),
			})

		case upd := <-g.windPowerChan:
			renewMW = upd.MW

		case ts := <-g.thermalChan:
			thermMW = ts.MW
			thermOn = ts.IsOn

		case fr := <-g.forecastChan:
			currentForecast = fr
			if fr.DeltaMW < -10 && !thermOn {
				g.logger.Log(LogEntry{
					TimeStep: time.Now().Unix(),
					Message:  fmt.Sprintf("Prognoza: spadek OZE o %.1f MW — uruchamiam elektrownię z wyprzedzeniem", fr.DeltaMW),
				})
				select {
				case g.startThermal <- struct{}{}:
				default:
				}
			}

		case dr := <-g.demandChan:
			pendingDemands[dr.ID] = dr

		case wd := <-g.weatherSub:
			lastWeather = wd

		case <-ticker.C:
			step++
			g.balance(ctx, step, consumers, pendingDemands, lastWeather, currentForecast, renewMW, thermMW)

			g.statsMu.Lock()
			g.totalSteps++
			g.statsMu.Unlock()
		}
	}
}

func (g *GridHub) batteryQuery(kind string, mw float64) BatteryResponse {
	replyCh := make(chan BatteryResponse, 1)
	g.battery.CmdChan() <- BatteryCommand{Kind: kind, MW: mw, Reply: replyCh}
	return <-replyCh
}

func (g *GridHub) balance(
	ctx context.Context,
	step int,
	consumers map[string]*consumerEntry,
	pending map[string]DemandReport,
	weather WeatherData,
	forecast ForecastReport,
	renewMW float64,
	thermMW float64,
) {
	for id, dr := range pending {
		if e, ok := consumers[id]; ok {
			e.lastDemand = dr
		}
	}
	for k := range pending {
		delete(pending, k)
	}

	type demandInfo struct {
		id       string
		priority int
		demand   float64
	}
	var demands []demandInfo
	totalDemand := 0.0
	for id, e := range consumers {
		if e.lastDemand.ID == "" {
			continue
		}
		demands = append(demands, demandInfo{
			id:       id,
			priority: e.lastDemand.Priority,
			demand:   e.lastDemand.MW,
		})
		totalDemand += e.lastDemand.MW
	}

	production := renewMW + thermMW
	balance := production - totalDemand

	socResp := g.batteryQuery("get_soc", 0)
	soc := socResp.SoC

	if balance > 0 {
		if soc < 1.0 {
			g.batteryQuery("charge", balance)
		} else {
			g.logger.Log(LogEntry{
				TimeStep: int64(step),
				Message:  fmt.Sprintf("Curtailment OZE: nadwyżka %.1f MW odrzucona (SoC=100%%)", balance),
			})
		}
	} else if balance < 0 {
		dischargeResp := g.batteryQuery("discharge", -balance)
		balance += dischargeResp.DelivMW
	}

	socAfter := g.batteryQuery("get_soc", 0).SoC

	shedOccurred := false
	shedSet := make(map[string]bool)

	if balance < 0 {
		sorted := make([]demandInfo, len(demands))
		copy(sorted, demands)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].priority > sorted[j].priority
		})

		for _, d := range sorted {
			if balance >= 0 {
				break
			}
			e := consumers[d.id]
			if e == nil {
				continue
			}
			select {
			case e.consumer.supplyChan <- SupplyStatus{AllocatedMW: 0, Reason: "LoadShed"}:
			case <-ctx.Done():
				return
			}
			shedSet[d.id] = true
			balance += d.demand
			shedOccurred = true

			g.statsMu.Lock()
			g.totalShed += d.demand
			g.statsMu.Unlock()

			g.logger.Log(LogEntry{
				TimeStep: int64(step),
				Message:  fmt.Sprintf("LoadShed: odłączono %s (priorytet %d, %.1f MW)", d.id, d.priority, d.demand),
			})
		}
	}

	networkState := "STABLE"
	if shedOccurred {
		networkState = "CRITICAL"
	}

	for _, d := range demands {
		if shedSet[d.id] {
			continue
		}
		e := consumers[d.id]
		if e == nil {
			continue
		}
		alloc := d.demand
		if balance < 0 {
			alloc = math.Max(0, d.demand+balance/float64(len(demands)-len(shedSet)))
		}
		select {
		case e.consumer.supplyChan <- SupplyStatus{AllocatedMW: alloc, Reason: networkState}:
		case <-ctx.Done():
			return
		}
	}

	g.logger.Log(LogEntry{
		TimeStep: int64(step),
		Message: fmt.Sprintf(
			"[Pogoda] Wiatr: %.1f km/h | Słońce: %.0f%%\n"+
				"[Produkcja] OZE: %.1f MW | Konwencjonalna: %.1f MW | Baterie: %.0f%% (SoC)\n"+
				"[Sieć] Popyt: %.1f MW | Bilans: %.1f MW | Stan: %s",
			weather.WindSpeed, weather.Sunlight,
			renewMW, thermMW, socAfter*100,
			totalDemand, balance, networkState,
		),
	})

	_ = forecast
	_ = soc
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
	return &CSVLogger{ch: make(chan LogEntry, 2000), file: f, w: w, wg: wg}, nil
}

func (l *CSVLogger) Run(ctx context.Context) {
	l.wg.Add(1)
	defer l.wg.Done()
	for {
		select {
		case e := <-l.ch:
			l.write(e)
		case <-ctx.Done():
			for {
				select {
				case e := <-l.ch:
					l.write(e)
				default:
					l.w.Flush()
					_ = l.file.Close()
					fmt.Println("[DataLogger] Plik CSV zamknięty, wszystkie dane zapisane.")
					return
				}
			}
		}
	}
}

func (l *CSVLogger) write(e LogEntry) {
	_ = l.w.Write([]string{fmt.Sprint(e.TimeStep), e.Message})
	fmt.Printf("[%d] %s\n", e.TimeStep, e.Message)
}

func (l *CSVLogger) Log(entry LogEntry) {
	select {
	case l.ch <- entry:
	default:
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())

	ctx, cancel := context.WithCancel(context.Background())
	wg := &sync.WaitGroup{}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	logger, err := NewCSVLogger("logs/grid.csv", wg)
	if err != nil {
		fmt.Println("Błąd loggera:", err)
		return
	}
	go logger.Run(ctx)

	weatherRaw := make(chan WeatherData, 10)
	weatherForFarm := make(chan WeatherData, 10)
	weatherForPredictor := make(chan WeatherData, 10)
	weatherForGrid := make(chan WeatherData, 10)

	forecastChan := make(chan ForecastReport, 1)
	demandChan := make(chan DemandReport, 100)

	windPowerChan := make(chan PowerUpdate, 10)
	thermalChan := make(chan ThermalStatus, 5)

	ws := NewWeatherStation(weatherRaw)
	bc := NewBroadcaster(weatherRaw, []chan WeatherData{
		weatherForFarm,
		weatherForPredictor,
		weatherForGrid,
	})
	wf := NewWindFarm(weatherForFarm, windPowerChan)
	coal := NewCoalPlant(150, thermalChan)
	batt := NewBattery(BatteryCapMW)
	pred := NewSimplePredictor(weatherForPredictor, forecastChan, logger)

	grid := NewGridHub(
		batt,
		forecastChan,
		demandChan,
		weatherForGrid,
		windPowerChan,
		thermalChan,
		coal.StartChan(),
		logger,
	)

	res := NewBaseConsumer("residential", 3, demandChan, logger)
	ind := NewBaseConsumer("industrial", 2, demandChan, logger)
	crit := NewBaseConsumer("critical", 1, demandChan, logger)

	grid.RegisterConsumer(res)
	grid.RegisterConsumer(ind)
	grid.RegisterConsumer(crit)

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

	fmt.Println("System energetyczny uruchomiony. Ctrl+C aby zatrzymać.")

	<-sigCh
	fmt.Println("\nZamykanie systemu...")
	cancel()
	wg.Wait()
	fmt.Println("System zamknięty. Dane zapisane w logs/grid.csv")
}
