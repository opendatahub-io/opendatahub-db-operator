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

package manager

// Option configures New. There are no generic options yet -- this exists so
// New's signature doesn't need to change when phase 2 adds its first one
// (e.g. a reconciler-scoped client/recorder override).
type Option interface {
	applyOption(*Options)
}

// Options holds the values Option implementations mutate. Empty for now;
// phase 2 is expected to add its first field here alongside the
// DatabaseService reconciler wiring.
type Options struct{}
