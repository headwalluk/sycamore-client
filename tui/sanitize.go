package main

import (
	"reflect"
	"strings"
)

// terminalSafe removes characters that a terminal would act on rather than
// print: C0 controls other than newline and tab, DEL, C1 controls and the
// bidirectional overrides. Invalid UTF-8 becomes U+FFFD.
func terminalSafe(text string) string {
	if isTerminalSafe(text) {
		return text
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for _, character := range strings.ToValidUTF8(text, "�") {
		if !unsafeRune(character) {
			builder.WriteRune(character)
		}
	}
	return builder.String()
}

// isTerminalSafe reports whether text is valid UTF-8 with no unsafe runes.
func isTerminalSafe(text string) bool {
	safe := true
	for _, character := range text {
		if character == '�' || unsafeRune(character) {
			safe = false
			break
		}
	}
	return safe
}

// unsafeRune reports whether a rune is a terminal control or a bidi override.
func unsafeRune(character rune) bool {
	switch {
	case character == '\n', character == '\t':
		return false
	case character < 0x20, character == 0x7f:
		return true
	case character >= 0x80 && character <= 0x9f:
		return true
	case character >= 0x202a && character <= 0x202e, character >= 0x2066 && character <= 0x2069:
		return true
	}
	return false
}

// sanitizeStrings applies terminalSafe to every string reachable from value,
// in place. value is a pointer (a decoded protocol message).
func sanitizeStrings(value any) {
	if value != nil {
		sanitizeValue(reflect.ValueOf(value))
	}
}

// sanitizeValue rewrites the strings under one reflected value. Values that
// can't be set in place (map entries, interface contents) are copied,
// cleaned and stored back.
func sanitizeValue(value reflect.Value) {
	switch value.Kind() {
	case reflect.Pointer:
		if !value.IsNil() {
			sanitizeValue(value.Elem())
		}
	case reflect.Interface:
		if !value.IsNil() && value.CanSet() {
			value.Set(cleanedCopy(value.Elem()))
		}
	case reflect.String:
		if value.CanSet() {
			value.SetString(terminalSafe(value.String()))
		}
	case reflect.Struct:
		for index := range value.NumField() {
			if value.Type().Field(index).IsExported() {
				sanitizeValue(value.Field(index))
			}
		}
	case reflect.Slice, reflect.Array:
		// Byte slices (json.RawMessage, image data) are never printed raw.
		if value.Type().Elem().Kind() != reflect.Uint8 {
			for index := range value.Len() {
				sanitizeValue(value.Index(index))
			}
		}
	case reflect.Map:
		if !value.IsNil() {
			for _, key := range value.MapKeys() {
				cleanedKey := cleanedCopy(key)
				cleanedEntry := cleanedCopy(value.MapIndex(key))
				value.SetMapIndex(key, reflect.Value{})
				value.SetMapIndex(cleanedKey, cleanedEntry)
			}
		}
	default:
		// Numbers and booleans carry no text.
	}
}

// cleanedCopy returns an addressable copy of value with its strings cleaned.
func cleanedCopy(value reflect.Value) reflect.Value {
	copied := reflect.New(value.Type()).Elem()
	copied.Set(value)
	sanitizeValue(copied)
	return copied
}
