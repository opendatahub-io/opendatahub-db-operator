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

// Temporary early-gate infrastructure smoke test only; do not merge.
func TestParseServerVersionNum(t *testing.T) {
	tests := []struct {
		name       string
		version    int
		want       Version
		wantString string
	}{
		{name: "pre-10 release", version: 90603, want: Version{Major: 9, Minor: 6, Patch: 3}, wantString: "9.6.3"},
		{name: "10 release", version: 100001, want: Version{Major: 10, Minor: 1}, wantString: "10.1"},
		{name: "modern release", version: 160004, want: Version{Major: 16, Minor: 4}, wantString: "16.4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := NewWithT(t)
			version := ParseServerVersionNum(tt.version)
			g.Expect(version).To(Equal(tt.want))
			g.Expect(version.String()).To(Equal(tt.wantString))
		})
	}
}

func TestMajorVersionRangeContains(t *testing.T) {
	g := NewWithT(t)
	range13Through17 := MajorVersionRange{Minimum: 13, Maximum: 17}
	g.Expect(range13Through17.Contains(Version{Major: 13})).To(BeTrue())
	g.Expect(range13Through17.Contains(Version{Major: 17, Minor: 9})).To(BeTrue())
	g.Expect(range13Through17.Contains(Version{Major: 12, Minor: 9})).To(BeFalse())
	g.Expect(range13Through17.Contains(Version{Major: 18})).To(BeFalse())
}

func TestServerVersionNumReadsLiveServer(t *testing.T) {
	g := NewWithT(t)
	cfg := startPingPostgres(t)
	client, err := NewClient(t.Context(), cfg)
	g.Expect(err).NotTo(HaveOccurred())
	t.Cleanup(client.Close)

	version, err := ServerVersion(t.Context(), client)
	g.Expect(err).NotTo(HaveOccurred())
	g.Expect(version.Major).To(Equal(16))
}
