package provider

import (
	"context"
	"errors"
)

// ErrManual is the error of each change at a provider without an API.
var ErrManual = errors.New("void provider has no API; make or revoke the key at the vendor, then run orkeys put")

// OptConsole is the option with the vendor page where a person makes and revokes keys. Events show it.
const OptConsole = "console"

var voidCaps = Caps{Manual: true}

var voidOptions = []string{OptConsole}

// void is a provider without an API. It has no keys to list and refuses all changes.
type void struct{}

func newVoid(Config) *void { return &void{} }

func (void) Name() string   { return "void" }
func (void) Caps() Caps     { return voidCaps }
func (void) SelfID() string { return "" }

// List returns no keys. orkeys cannot see the keys of the vendor.
func (void) List(context.Context) ([]Key, error) { return nil, nil }

func (void) Create(context.Context, Spec) (Key, string, error) { return Key{}, "", ErrManual }

// Delete returns ErrManual. A void key stays at the vendor until a person revokes it.
func (void) Delete(context.Context, string) error { return ErrManual }
