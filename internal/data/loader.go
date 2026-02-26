package data

import (
	_ "embed"
)

//go:embed simulation.json
var SimulationJSON []byte

func GetSimulationData() []byte {
	return SimulationJSON
}
