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

package postgres

import (
	"testing"

	. "github.com/onsi/gomega"
)

func TestFormatServerVersion(t *testing.T) {
	tests := []struct {
		name    string
		version int
		want    string
	}{
		{name: "pre-10 release", version: 90603, want: "9.6.3"},
		{name: "10 release", version: 100001, want: "10.1"},
		{name: "modern release", version: 160004, want: "16.4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			g.Expect(FormatServerVersion(tt.version)).To(Equal(tt.want))
		})
	}
}

func TestServerVersionNumReadsLiveServer(t *testing.T) {
	g := NewWithT(t)
	cfg := startPingPostgres(t)
	client, err := NewClient(t.Context(), cfg)
	g.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(client.Close)

	version, err := ServerVersionNum(t.Context(), client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(version).To(BeNumerically(">=", 160000))
	g.Expect(version).To(BeNumerically("<", 170000))
}
