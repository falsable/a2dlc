package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestCompareHTTPSequencesStatefulPassAndFail(t *testing.T) {
	// 1. Stateful matching servers -> PASS
	var baseCount, candCount atomic.Int32
	baseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/items" {
			baseCount.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":true}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count":1}`))
	}))
	defer baseSrv.Close()

	candSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/items" {
			candCount.Add(1)
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created": true}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count": 1}`))
	}))
	defer candSrv.Close()

	seqs := [][]HTTPStep{{
		{Method: http.MethodPost, Path: "/items", Body: `{"name":"x"}`},
		{Method: http.MethodGet, Path: "/items"},
	}}
	v, ce, err := CompareHTTPSequences(context.Background(), baseSrv.URL, candSrv.URL, seqs)
	if err != nil || v != contract.PASS || ce != nil {
		t.Fatalf("got v=%q ce=%+v err=%v", v, ce, err)
	}

	// 2. Diverging candidate -> FAIL with Counterexample
	brokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":true}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"count":2}`))
	}))
	defer brokenSrv.Close()

	v2, ce2, err2 := CompareHTTPSequences(context.Background(), baseSrv.URL, brokenSrv.URL, seqs)
	if err2 != nil || v2 != contract.FAIL || ce2 == nil {
		t.Fatalf("got v2=%q ce2=%+v err2=%v", v2, ce2, err2)
	}
}
