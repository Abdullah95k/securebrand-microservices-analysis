package pipeline_test

import (
	"errors"
	"strconv"
	"strings"
)

var errOverloaded = errors.New("overloaded")

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func itoa(i int) string { return strconv.Itoa(i) }
