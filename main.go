package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v2"
)

// DSNMapping represents one mapping from the config file.
type DSNMapping struct {
	Old string `yaml:"old"`
	New string `yaml:"new"`
}

// Config represents the structure of config.yaml.
type Config struct {
	DSNMapping []DSNMapping `yaml:"dsn_mapping"`
}

// Mapping holds parsed DSN URLs for easier access.
type Mapping struct {
	OldURI *url.URL
	NewURI *url.URL
	OldDSN string
	NewDSN string
}

var mappings []DSNMapping

// loadConfig reads and unmarshals the YAML configuration.
func loadConfig(path string) (*Config, error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(data, &config)
	return &config, err
}

// getOldKey extracts the "sentry_key" from the X-Sentry-Auth header.
// The header is expected to be in a comma-separated list of key=value pairs.
func getOldKey(headerValue string) string {
	parts := strings.Split(headerValue, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if keyVal := strings.SplitN(part, "=", 2); len(keyVal) == 2 {
			key := strings.TrimSpace(keyVal[0])
			value := strings.Trim(strings.TrimSpace(keyVal[1]), `"`)
			if key == "sentry_key" {
				return value
			}
		}
	}
	return ""
}

// getMapping finds the mapping whose old DSN user matches oldKey.
func getMapping(oldKey string, mappings []DSNMapping) *Mapping {
	for _, m := range mappings {
		oldURI, err := url.Parse(m.Old)
		if err != nil || oldURI.User == nil {
			continue
		}
		if oldURI.User.Username() == oldKey {
			newURI, err := url.Parse(m.New)
			if err != nil {
				continue
			}
			return &Mapping{
				OldURI: oldURI,
				NewURI: newURI,
				OldDSN: m.Old,
				NewDSN: m.New,
			}
		}
	}
	return nil
}

// convertPayload decompresses the gzip payload, replaces the old DSN and user key with the new ones,
// then recompresses the payload using gzip.
func convertPayload(payload []byte, mapping *Mapping) ([]byte, error) {
	// Decompress gzip data.
	gzReader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	decompressed, err := ioutil.ReadAll(gzReader)
	gzReader.Close()
	if err != nil {
		return nil, err
	}
	s := string(decompressed)

	// Escape the DSNs by replacing "/" with "\/".
	escapedOldDSN := strings.ReplaceAll(mapping.OldDSN, "/", `\/`)
	escapedNewDSN := strings.ReplaceAll(mapping.NewDSN, "/", `\/`)
	s = strings.ReplaceAll(s, escapedOldDSN, escapedNewDSN)

	// Replace the old user key with the new one.
	if mapping.OldURI.User != nil && mapping.NewURI.User != nil {
		oldUser := mapping.OldURI.User.Username()
		newUser := mapping.NewURI.User.Username()
		s = strings.ReplaceAll(s, oldUser, newUser)
	}

	// Compress back to gzip.
	var buf bytes.Buffer
	gzWriter := gzip.NewWriter(&buf)
	_, err = gzWriter.Write([]byte(s))
	if err != nil {
		gzWriter.Close()
		return nil, err
	}
	gzWriter.Close()
	return buf.Bytes(), nil
}

// handler processes incoming requests, rewrites headers and payload,
// then forwards the request to the new Sentry DSN.
func handler(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{}

	// Extract the old Sentry key.
	sentryAuth := r.Header.Get("X-Sentry-Auth")
	oldKey := getOldKey(sentryAuth)
	mapping := getMapping(oldKey, mappings)
	if mapping == nil {
		log.Printf("Unknown old sentry DSN key: %s", oldKey)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": "unknown DSN for forwarding"})
		return
	}

	// Copy and modify headers.
	newHeaders := make(http.Header)
	for k, values := range r.Header {
		if len(values) > 0 {
			newHeaders.Set(k, values[0])
		}
	}
	if mapping.OldURI.User != nil && mapping.NewURI.User != nil {
		oldUser := mapping.OldURI.User.Username()
		newUser := mapping.NewURI.User.Username()
		newAuth := strings.ReplaceAll(newHeaders.Get("X-Sentry-Auth"), oldUser, newUser)
		newHeaders.Set("X-Sentry-Auth", newAuth)
	}
	newHeaders.Set("Host", mapping.NewURI.Host)

	// Construct the new URL.
	newURL := mapping.NewURI.Scheme + "://" + mapping.NewURI.Host + "/api" + mapping.NewURI.Path + "/envelope/"
	log.Printf("Forwarding from %s to %s", mapping.OldDSN, mapping.NewDSN)

	// Read the incoming request body.
	body, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	// Convert (rewrite) the payload.
	newBody, err := convertPayload(body, mapping)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Create the new POST request.
	req, err := http.NewRequest("POST", newURL, bytes.NewReader(newBody))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req.Header = newHeaders

	// Forward the request.
	resp, err := client.Do(req)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()

	// Write the response.
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)
}

func main() {
	// Load the configuration.
	config, err := loadConfig("config.yaml")
	if err != nil {
		log.Fatalf("Error reading config: %v", err)
	}
	mappings = config.DSNMapping

	// Set the port from environment or default to 8000.
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	// Set up the HTTP server.
	http.HandleFunc("/", handler)
	log.Printf("Listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
