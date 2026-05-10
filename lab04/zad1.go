package main

import (
	"context"
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

type EnergyStorage interface {
	Charge(mw float64)
	Discharge(mw float64) float64
	SoC() float64
}

type WeatherProvider interface {
	Run(ctx context.Context)
	Subscribe() <-chan WeatherData
}

type DataLogger interface {
	Run(ctx context.Context)
	Log(entry LogEntry)
}

type Channels struct {
	WeatherPub   []chan WeatherData
	ForecastChan chan ForecastReport
	DemandChan   chan DemandReport
	LoggerChan   chan LogEntry
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
