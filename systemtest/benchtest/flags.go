// Licensed to Elasticsearch B.V. under one or more contributor
// license agreements. See the NOTICE file distributed with
// this work for additional information regarding copyright
// ownership. Elasticsearch B.V. licenses this file to you under
// the Apache License, Version 2.0 (the "License"); you may
// not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package benchtest

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	server        = flag.String("server", getenvDefault("ELASTIC_APM_SERVER_URL", "http://localhost:8200"), "apm-server URL")
	count         = flag.Uint("count", 1, "run benchmarks `n` times")
	agentsListStr = flag.String("agents", "1", "comma-separated `list` of agent counts to run each benchmark with")
	benchtime     = flag.Duration("benchtime", time.Second, "run each benchmark for duration `d`")
	secretToken   = flag.String("secret-token", os.Getenv("ELASTIC_APM_SECRET_TOKEN"), "secret token for APM Server")
	match         = flag.String("run", "", "run only benchmarks matching `regexp`")
	secure        = flag.Bool("secure", boolFromEnv("ELASTIC_APM_VERIFY_SERVER_CERT", false), "validate the remote server TLS certificates")

	cpuprofile   = flag.String("cpuprofile", "", "Write a CPU profile to the specified file before exiting.")
	memprofile   = flag.String("memprofile", "", "Write an allocation profile to the file  before exiting.")
	mutexprofile = flag.String("mutexprofile", "", "Write a mutex contention profile to the file  before exiting.")
	blockprofile = flag.String("blockprofile", "", "Write a goroutine blocking profile to the file before exiting.")

	warmupEvents = flag.Uint("warmup-events", 5000, "The number of events that will be used to warm up the APM Server before each benchmark")
	maxRate      = flag.String("max-rate", "-1eps", "Max event rate with a burst size of max(1000, 2*eps), >= 0 values evaluate to Inf")
	detailed     = flag.Bool("detailed", false, "Get detailed metrics recorded during benchmark")

	maxEPM     int
	agentsList []int
	serverURL  *url.URL
	runRE      *regexp.Regexp
)

func getenvDefault(name, defaultValue string) string {
	value := os.Getenv(name)
	if value != "" {
		return value
	}
	return defaultValue
}

func parseFlags() error {
	flag.Parse()

	// Parse -agents.
	agentsList = nil
	for _, val := range strings.Split(*agentsListStr, ",") {
		val = strings.TrimSpace(val)
		if val == "" {
			continue
		}
		n, err := strconv.Atoi(val)
		if err != nil || n <= 0 {
			return fmt.Errorf("invalid value %q for -agents", val)
		}
		agentsList = append(agentsList, n)
	}

	// Parse -server.
	u, err := url.Parse(*server)
	if err != nil {
		return err
	}
	serverURL = u

	// Parse -run.
	if *match != "" {
		re, err := regexp.Compile(*match)
		if err != nil {
			return err
		}
		runRE = re
	} else {
		runRE = regexp.MustCompile(".")
	}

	// Parse -max-rate
	errStr := "invalid value %s for -max-rate, valid examples: 5eps or 10epm"
	r := strings.Split(*maxRate, "ep")
	if len(r) != 2 {
		return fmt.Errorf(errStr, *maxRate)
	}
	rateVal, err := strconv.Atoi(r[0])
	if err != nil {
		return fmt.Errorf(errStr, *maxRate)
	}
	switch r[1] {
	case "s":
		maxEPM = rateVal * 60
	case "m":
		maxEPM = rateVal
	default:
		return fmt.Errorf(errStr, *maxRate)
	}

	// Set flags in package testing.
	testing.Init()
	if err := flag.Set("test.benchtime", benchtime.String()); err != nil {
		return err
	}
	return nil
}

func boolFromEnv(varName string, defaultVal bool) bool {
	envVal := os.Getenv(varName)
	if envVal == "" {
		return defaultVal
	}

	b, err := strconv.ParseBool(envVal)
	if err != nil {
		panic(fmt.Sprintf("failed parsing bool environment %s: %s",
			varName, err.Error(),
		))
	}
	return b

}
