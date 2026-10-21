package app

import (
	"testing"

	"github.com/sid/harness/internal/stream"
)

func TestBusInfo(t *testing.T) {
	b := stream.New()
	if err := b.Start(); err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	info := NewBusService(b).BusInfo()
	if info.Port == 0 || len(info.Token) != 64 {
		t.Fatalf("%+v", info)
	}
}
