package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGetItems_SendsGETAndDecodesJSON(t *testing.T) {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/items" {
			t.Errorf("expected /items, got %s", r.URL.Path)
		}
		ua := r.Header.Get("User-Agent")
		if ua != "go-http-clientPV" {
			t.Errorf("expected User-Agent go-http-clientPV, got %q", ua)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[
            {"id":1,"name":"First item","description":"Example description"},
            {"id":2,"name":"Second item","description":"Another example description"}
        ]`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	ctx := context.Background()
	items, err := client.GetItems(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	if items[0].ID != 1 || items[0].Name != "First item" {
		t.Errorf("unexpected first item: %+v", items[0])
	}
}

func TestGetItems_UnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := NewClient(server.URL)

	_, err := client.GetItems(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unexpected status code") {
		t.Fatalf("expected unexpected status code error, got %v", err)
	}
}

func TestCreateItem_SendsPOSTAndEncodesJSONAndDecodesResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/items" {
			t.Errorf("expected /items, got %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected Content-Type application/json, got %q", ct)
		}
		ua := r.Header.Get("User-Agent")
		if ua != "go-http-clientPV" {
			t.Errorf("expected User-Agent go-http-clientPV, got %q", ua)
		}

		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		var reqBody CreateItemRequest
		if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		if reqBody.Name != "New item" || reqBody.Description != "Description of the new item" {
			t.Errorf("unexpected request body: %+v", reqBody)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{
            "id":3,
            "name":"New item",
            "description":"Description of the new item"
        }`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	ctx := context.Background()
	item, err := client.CreateItem(ctx, CreateItemRequest{
		Name:        "New item",
		Description: "Description of the new item",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if item.ID != 3 || item.Name != "New item" {
		t.Errorf("unexpected created item: %+v", item)
	}
}

func TestCreateItem_UnexpectedStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	client := NewClient(server.URL)

	_, err := client.CreateItem(context.Background(), CreateItemRequest{
		Name:        "X",
		Description: "Y",
	})
	if err == nil || !strings.Contains(err.Error(), "unexpected status code") {
		t.Fatalf("expected unexpected status code error, got %v", err)
	}
}

func TestContextIsUsedAndCanTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer server.Close()

	client := NewClient(server.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.GetItems(ctx)
	if err == nil {
		t.Fatalf("expected context deadline exceeded error, got nil")
	}
}
