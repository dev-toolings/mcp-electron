package flow

import rodinput "github.com/go-rod/rod/lib/input"

// input maps the few named keys the runner dispatches itself.
func input(name string) rodinput.Key {
	switch name {
	case "Backspace":
		return rodinput.Backspace
	}
	return rodinput.Enter
}
