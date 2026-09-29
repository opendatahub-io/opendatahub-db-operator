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

package config

import (
	"io/fs"

	"github.com/spf13/viper"
)

// Option is implemented by both the LoadOptions struct literal and the named
// With* constructor functions.
type Option interface {
	applyOption(o *LoadOptions)
}

// LoadOptions configures how Load obtains the effective Config.
type LoadOptions struct {
	Viper      *viper.Viper
	FS         fs.FS
	ConfigPath string
}

func (o LoadOptions) applyOption(target *LoadOptions) {
	if o.Viper != nil {
		target.Viper = o.Viper
	}
	if o.FS != nil {
		target.FS = o.FS
	}
	if o.ConfigPath != "" {
		target.ConfigPath = o.ConfigPath
	}
}

type optionFunc func(*LoadOptions)

func (fn optionFunc) applyOption(target *LoadOptions) {
	if fn == nil {
		return
	}
	fn(target)
}

func WithViper(v *viper.Viper) Option {
	return optionFunc(func(options *LoadOptions) {
		if options == nil || v == nil {
			return
		}
		options.Viper = v
	})
}

func WithFS(fsys fs.FS) Option {
	return optionFunc(func(options *LoadOptions) {
		if options == nil || fsys == nil {
			return
		}
		options.FS = fsys
	})
}

func WithConfigPath(path string) Option {
	return optionFunc(func(options *LoadOptions) {
		if options == nil || path == "" {
			return
		}
		options.ConfigPath = path
	})
}
