package app

import "github.com/sid/harness/internal/stream"

// BusInfo is what the frontend needs to connect to the bus:
// ws://127.0.0.1:<Port>/bus?token=<Token>.
type BusInfo struct {
	Port  int    `json:"port"`
	Token string `json:"token"`
}

// BusService exposes the stream bus connection details as a Wails binding.
type BusService struct {
	bus *stream.Bus
}

func NewBusService(bus *stream.Bus) *BusService { return &BusService{bus: bus} }

// BusInfo returns the loopback port and auth token.
func (s *BusService) BusInfo() BusInfo {
	return BusInfo{Port: s.bus.Port(), Token: s.bus.Token()}
}
