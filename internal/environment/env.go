package env

import (
	"github.com/caarlos0/env/v11"
)

func GetConf[T any]() *T {
	var configuration T
	err := env.Parse(&configuration)
	if err != nil {
		panic(err)
	}
	return &configuration
}

// GetDefaultConf returns a configuration that contains only the envDefault
// values of T and ignores the process environment. It is meant as a
// deterministic starting point for tests and explicit configurations.
func GetDefaultConf[T any]() *T {
	var configuration T
	err := env.ParseWithOptions(&configuration, env.Options{Environment: map[string]string{}})
	if err != nil {
		panic(err)
	}
	return &configuration
}
