package main

import (
	"reflect"
	"testing"
)

func TestEnvFlag(t *testing.T) {
	var e envFlag
	for _, v := range []string{"A=1", "B=x,y=z", "C="} {
		if err := e.Set(v); err != nil {
			t.Fatal(err)
		}
	}
	if want := map[string]string{"A": "1", "B": "x,y=z", "C": ""}; !reflect.DeepEqual(e.vars, want) {
		t.Fatalf("exec env %v", e.vars)
	}
	if err := e.Set("NOEQUALS"); err == nil {
		t.Fatal("accepted a value without =")
	}

	legacy := envFlag{splitCommas: true}
	if err := legacy.Set("A=1,B=2"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Set("C=3"); err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"A": "1", "B": "2", "C": "3"}; !reflect.DeepEqual(legacy.vars, want) {
		t.Fatalf("push env %v", legacy.vars)
	}
}
