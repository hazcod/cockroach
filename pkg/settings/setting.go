// Copyright 2017 The Cockroach Authors.
//
// Use of this software is governed by the Business Source License
// included in the file licenses/BSL.txt.
//
// As of the Change Date specified in that file, in accordance with
// the Business Source License, use of this software will be governed
// by the Apache License, Version 2.0, included in the file
// licenses/APL.txt.

package settings

import (
	"fmt"
	"sync/atomic"

	"github.com/cockroachdb/cockroach/pkg/util/syncutil"
)

const maxSettings = 128

// Values is a container that stores values for all registered settings.
// Each setting is assigned a unique slot (up to maxSettings).
// Note that slot indices are 1-based (this is to trigger panics if an
// uninitialized slot index is used).
type Values struct {
	intVals     [maxSettings]int64
	genericVals [maxSettings]atomic.Value

	changeMu struct {
		syncutil.Mutex
		// NB: any in place modification to individual slices must also hold the
		// lock, e.g. if we ever add RemoveOnChange or something.
		onChange [maxSettings][]func()
	}
	// opaque is an arbitrary object that can be set by a higher layer to make it
	// accessible from certain callbacks (like state machine transformers).
	opaque interface{}
}

var (
	canonicalValues atomic.Value
)

// TODO is usable at callsites that do not have *settings.Values available.
// Please don't use this.
func TODO() *Values {
	if ptr := canonicalValues.Load(); ptr != nil {
		return ptr.(*Values)
	}
	return nil
}

// SetCanonicalValuesContainer sets the Values container that will be refreshed
// at runtime -- ideally we should have no other *Values containers floating
// around, as they will be stale / lies.
func SetCanonicalValuesContainer(v *Values) {
	canonicalValues.Store(v)
}

type testOpaqueType struct{}

// TestOpaque can be passed to Values.Init when we are testing the settings
// infrastructure.
var TestOpaque interface{} = testOpaqueType{}

// Init must be called before using a Values instance; it initializes all
// variables to their defaults.
//
// The opaque argument can be retrieved later via Opaque().
func (sv *Values) Init(opaque interface{}) {
	sv.opaque = opaque
	for _, s := range Registry {
		s.setToDefault(sv)
	}
}

// Opaque returns the argument passed to Init.
func (sv *Values) Opaque() interface{} {
	return sv.opaque
}

func (sv *Values) settingChanged(slotIdx int) {
	sv.changeMu.Lock()
	funcs := sv.changeMu.onChange[slotIdx-1]
	sv.changeMu.Unlock()
	for _, fn := range funcs {
		fn()
	}
}

func (sv *Values) getInt64(slotIdx int) int64 {
	return atomic.LoadInt64(&sv.intVals[slotIdx-1])
}

func (sv *Values) setInt64(slotIdx int, newVal int64) {
	if atomic.SwapInt64(&sv.intVals[slotIdx-1], newVal) != newVal {
		sv.settingChanged(slotIdx)
	}
}

func (sv *Values) getGeneric(slotIdx int) interface{} {
	return sv.genericVals[slotIdx-1].Load()
}

func (sv *Values) setGeneric(slotIdx int, newVal interface{}) {
	sv.genericVals[slotIdx-1].Store(newVal)
	sv.settingChanged(slotIdx)
}

// setOnChange installs a callback to be called when a setting's value changes.
// `fn` should avoid doing long-running or blocking work as it is called on the
// goroutine which handles all settings updates.
func (sv *Values) setOnChange(slotIdx int, fn func()) {
	sv.changeMu.Lock()
	sv.changeMu.onChange[slotIdx-1] = append(sv.changeMu.onChange[slotIdx-1], fn)
	sv.changeMu.Unlock()
}

// Setting is a descriptor for each setting; once it is initialized, it is
// immutable. The values for the settings are stored separately, in
// Values. This way we can have a global set of registered settings, each
// with potentially multiple instances.
type Setting interface {
	setToDefault(sv *Values)
	// Typ returns the short (1 char) string denoting the type of setting.
	Typ() string
	String(sv *Values) string
	Encoded(sv *Values) string

	EncodedDefault() string

	Description() string
	setDescription(desc string)
	setSlotIdx(slotIdx int)
	Hidden() bool

	SetOnChange(sv *Values, fn func())
}

type common struct {
	description string
	hidden      bool
	// Each setting has a slotIdx which is used as a handle with Values.
	slotIdx int
}

func (i *common) setSlotIdx(slotIdx int) {
	if slotIdx < 1 {
		panic(fmt.Sprintf("Invalid slot index %d", slotIdx))
	}
	if slotIdx > maxSettings {
		panic(fmt.Sprintf("too many settings; increase maxSettings"))
	}
	i.slotIdx = slotIdx
}

func (i *common) setDescription(s string) {
	i.description = s
}

func (i common) Description() string {
	return i.description
}
func (i common) Hidden() bool {
	return i.hidden
}

// SetConfidential prevents a setting from showing up in SHOW ALL
// CLUSTER SETTINGS. It can still be used with SET and SHOW if the
// exact setting name is known. Use SetConfidential for data that must
// be hidden from standard setting report and troubleshooting
// screenshots, such as license data or keys.
func (i *common) SetConfidential() {
	i.hidden = true
}

// SetSensitive marks the setting as dangerous to modify. Use SetConfidential for settings
// where the user must be strongly discouraged to tweak the values.
func (i *common) SetSensitive() {
	i.description += " (WARNING: may compromise cluster stability or correctness; do not edit without supervision)"
}

// SetDeprecated marks the setting as obsolete. It also hides
// it from the output of SHOW CLUSTER SETTINGS.
func (i *common) SetDeprecated() {
	i.description = "do not use - " + i.description
	i.hidden = true
}

// SetOnChange installs a callback to be called when a setting's value changes.
// `fn` should avoid doing long-running or blocking work as it is called on the
// goroutine which handles all settings updates.
func (i *common) SetOnChange(sv *Values, fn func()) {
	sv.setOnChange(i.slotIdx, fn)
}

type numericSetting interface {
	Setting
	Validate(i int64) error
	set(sv *Values, i int64) error
}
