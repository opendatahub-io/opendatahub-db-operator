/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package e2e holds this module's end-to-end test suite, run against an
// operator deployed via the Helm chart (config/chart) onto a real cluster.
//
// Phase 1 (RHOAIENG-96274) has no chart to deploy yet -- that's phase 3 -- so
// this suite is skeleton only: the TestMain wiring a real e2e suite will
// need (Gomega defaults, a direct client, an operator-deployment readiness
// gate) without any assertions yet. RHOAIENG-96277 and phase 3 fill this in
// once there's a CRD and a deployable chart to test against.
package e2e

import (
	"testing"

	. "github.com/onsi/gomega"

	"github.com/opendatahub-io/opendatahub-db-operator/test/support"
)

func TestMain(m *testing.M) {
	gomegaCfg := support.LoadGomegaConfig()
	SetDefaultEventuallyTimeout(gomegaCfg.EventuallyTimeout)
	SetDefaultEventuallyPollingInterval(gomegaCfg.EventuallyPollingInterval)
	SetDefaultConsistentlyPollingInterval(gomegaCfg.ConsistentlyPollingInterval)

	m.Run()
}

// TestPlaceholder exists so `go test ./test/e2e/...` has something to run
// without failing on "no tests to run" until phase 3 adds a deployed
// operator to gate real e2e tests on.
func TestPlaceholder(t *testing.T) {
	t.Skip("no deployable chart until phase 3 (RHOAIENG-96274 phase 1 scaffold only)")
}
