package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

func decodeRequest(r *http.Request, limit int64, target any) error {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errInvalidJSON
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(data)) > limit || (!utf8.Valid(data) || !validJSONScalars(data)) {
		return errInvalidJSON
	}
	if err := rejectDuplicateFields(data); err != nil {
		return errInvalidJSON
	}
	var raw any
	if err := json.Unmarshal(data, &raw); err != nil || validateJSONKeys(raw, reflect.TypeOf(target)) != nil {
		return errInvalidJSON
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errInvalidJSON
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errInvalidJSON
	}
	return nil
}

func validateJSONKeys(value any, target reflect.Type) error {
	for target.Kind() == reflect.Pointer {
		target = target.Elem()
	}
	switch target.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return errInvalidJSON
		}
		fields := make(map[string]reflect.Type, target.NumField())
		for index := 0; index < target.NumField(); index++ {
			field := target.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if name == "" {
				name = field.Name
			}
			if name != "-" {
				fields[name] = field.Type
			}
		}
		for key, child := range object {
			fieldType, exists := fields[key]
			if !exists {
				return errInvalidJSON
			}
			if err := validateJSONKeys(child, fieldType); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		array, ok := value.([]any)
		if !ok {
			return errInvalidJSON
		}
		for _, child := range array {
			if err := validateJSONKeys(child, target.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

func rejectDuplicateFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return errInvalidJSON
	}
	if err := scanObject(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errInvalidJSON
	}
	return nil
}

func scanObject(decoder *json.Decoder) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok {
			return errInvalidJSON
		}
		if _, exists := seen[key]; exists {
			return errInvalidJSON
		}
		seen[key] = struct{}{}
		if err := scanValue(decoder); err != nil {
			return err
		}
	}
	_, err := decoder.Token()
	return err
}

func scanValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errInvalidJSON
	}
	if token != json.Delim('{') && token != json.Delim('[') {
		return nil
	}
	if token == json.Delim('{') {
		return scanObject(decoder)
	}
	for decoder.More() {
		if err := scanValue(decoder); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func requiredString(value *string) (string, error) {
	if value == nil {
		return "", errInvalidJSON
	}
	return *value, nil
}

func requiredInt64(value *string) (int64, error) {
	text, err := requiredString(value)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != text {
		return 0, errInvalidJSON
	}
	return parsed, nil
}

func requiredTime(value *string) (time.Time, error) {
	text, err := requiredString(value)
	if err != nil || !moscowTimestamp.MatchString(text) {
		return time.Time{}, errInvalidJSON
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil || parsed.Format("-07:00") != "+03:00" || len(text) < 20 {
		return time.Time{}, errInvalidJSON
	}
	return parsed, nil
}

func queryValues(r *http.Request, allowed ...string) (url.Values, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, errInvalidQuery
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := allowedSet[key]; !ok || len(entries) != 1 {
			return nil, errInvalidQuery
		}
	}
	return values, nil
}

func requiredQuery(values url.Values, key string) (string, error) {
	entries, found := values[key]
	if !found || len(entries) != 1 || entries[0] == "" {
		return "", errInvalidQuery
	}
	return entries[0], nil
}

var (
	errInvalidJSON  = fmt.Errorf("invalid json request")
	errInvalidQuery = fmt.Errorf("invalid query")
)

var moscowTimestamp = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])-(0[1-9]|[12][0-9]|3[01])T([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](\.[0-9]{1,9})?\+03:00$`)

func validJSONScalars(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		i++
		for i < len(data) && data[i] != '"' {
			if data[i] != '\\' {
				i++
				continue
			}
			i++
			if i >= len(data) {
				return false
			}
			if data[i] != 'u' {
				i++
				continue
			}
			code, ok := jsonCodeUnit(data, i+1)
			if !ok {
				return false
			}
			i += 5
			if code >= 0xd800 && code <= 0xdbff {
				if i+6 > len(data) || data[i] != '\\' || data[i+1] != 'u' {
					return false
				}
				low, ok := jsonCodeUnit(data, i+2)
				if !ok || low < 0xdc00 || low > 0xdfff {
					return false
				}
				i += 6
			} else if code >= 0xdc00 && code <= 0xdfff {
				return false
			}
		}
	}
	return true
}
func jsonCodeUnit(data []byte, start int) (uint64, bool) {
	if start+4 > len(data) {
		return 0, false
	}
	value, err := strconv.ParseUint(string(data[start:start+4]), 16, 16)
	return value, err == nil
}
