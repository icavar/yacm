// Package toast is a no-op stand-in; see README.md.
package toast

import "errors"

// ActivationType mirrors the upstream type.
type ActivationType string

// Foreground activation.
const Foreground ActivationType = "foreground"

// AppData mirrors the upstream type.
type AppData struct {
	AppID         string
	GUID          string
	IconPath      string
	ActivationExe string
}

// UserData mirrors the upstream type.
type UserData struct {
	Key   string
	Value string
}

// Action mirrors the upstream type.
type Action struct {
	Content   string
	Arguments string
	InputID   string
}

// Input mirrors the upstream type.
type Input struct {
	ID          string
	Placeholder string
}

// Notification mirrors the upstream type.
type Notification struct {
	Title               string
	Body                string
	ActivationType      ActivationType
	ActivationArguments string
	Actions             []Action
	Inputs              []Input
}

// ErrUnsupported is returned by Push.
var ErrUnsupported = errors.New("toast notifications are disabled in this build")

// SetAppData is a no-op.
func SetAppData(AppData) error { return nil }

// SetActivationCallback is a no-op.
func SetActivationCallback(func(args string, data []UserData)) {}

// Push does nothing.
func (Notification) Push() error { return ErrUnsupported }
