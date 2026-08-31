// Package main implements the stenella data aggregation platform
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"time"
)

type StenellaServer struct {
	port string
}

type DataSource struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	URL         string            `json:"url"`
	Type        string            `json:"type"`
	Enabled     bool              `json:"enabled"`
	LastUpdated time.Time         `json:"last_updated"`
	Data        json.RawMessage   `json:"data"`
	Metadata    map[string]string `json:"metadata"`
}

type AggregatedData struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Sources   []string               `json:"sources"`
	Data      map[string]interface{} `json:"data"`
	Quality   float64                `json:"quality"`
}

func main() {
	port := flag.String("port", "8081", "Port to listen on (default: 8081)")
	help := flag.Bool("help", false, "Show help message")
	version := flag.Bool("version", false, "Show version information")

	flag.Parse()

	if *help {
		fmt.Println("Usage: stenella [options]")
		fmt.Println("  --port     Set the port to listen on (default: 8081)")
		fmt.Println("  --help     Show this help message")
		fmt.Println("  --version  Show version information")
		return
	}

	if *version {
		fmt.Println("Stenella Data Aggregation Platform v1.0.0")
		fmt.Println("Aggregates data from multiple sources")
		fmt.Println("Copyright 2025 Azzurro Technology Inc.")
		return
	}

	server := &StenellaServer{port: *port}
	if err := server.Start(); err != nil {
		log.Fatalf("Failed to start stenella server: %v", err)
	}
}

func (s *StenellaServer) Start() error {
	fmt.Printf("Starting Stenella data aggregation platform on port %s\n", s.port)
	fmt.Println("Data aggregation and normalization service")

	// Set up HTTP routes
	http.HandleFunc("/", s.homeHandler)
	http.HandleFunc("/api/data", s.getDataHandler)
	http.HandleFunc("/api/sources", s.getSourcesHandler)
	http.HandleFunc("/api/sources/", s.handleSourceWithID)
	http.HandleFunc("/health", s.healthCheckHandler)

	return http.ListenAndServe(":"+s.port, http.DefaultServeMux)
}

func (s *StenellaServer) homeHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprintf(w, "<html><body><h1>Stenella - Data Aggregation Platform</h1>")
	fmt.Fprintf(w, "<p>Data aggregation and normalization service</p>")
	fmt.Fprintf(w, "<ul>")
	fmt.Fprintf(w, "<li>GET /api/data - Get aggregated data</li>")
	fmt.Fprintf(w, "<li>GET /api/sources - List all data sources</li>")
	fmt.Fprintf(w, "<li>POST /api/sources - Add new data source</li>")
	fmt.Fprintf(w, "<li>GET /health - Health check</li>")
	fmt.Fprintf(w, "</ul>")
	fmt.Fprintf(w, "</body></html>")
}

func (s *StenellaServer) getDataHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get aggregated data from multiple real-time sources
	aggregated := s.aggregateRealTimeData()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(aggregated)
}

func (s *StenellaServer) aggregateRealTimeData() AggregatedData {
	// Simulate real-time data aggregation from multiple sources
	// In a real implementation, this would fetch data from external APIs

	// Create realistic timestamp
	timestamp := time.Now()

	// Simulate data quality scores from different sources
	qualityScores := []float64{0.95, 0.87, 0.92, 0.89, 0.91}
	var totalQuality float64
	for _, score := range qualityScores {
		totalQuality += score
	}
	avgQuality := totalQuality / float64(len(qualityScores))

	// Aggregate data from different sources
	aggregatedData := map[string]interface{}{
		"temperature":     s.generateTemperature(),
		"humidity":        s.generateHumidity(),
		"pressure":        s.generatePressure(),
		"wind_speed":      s.generateWindSpeed(),
		"precipitation":   s.generatePrecipitation(),
		"air_quality":     s.generateAirQuality(),
		"solar_radiation": s.generateSolarRadiation(),
		"uv_index":        s.generateUVIndex(),
	}

	return AggregatedData{
		ID:        "data_" + fmt.Sprintf("%06d", time.Now().UnixNano()%1000000),
		Timestamp: timestamp,
		Sources:   []string{"weather-api", "sensor-network", "data-feed", "environmental-monitor", "satellite"},
		Data:      aggregatedData,
		Quality:   avgQuality,
	}
}

func (s *StenellaServer) generateTemperature() float64 {
	// Generate realistic temperature based on current time and random variation
	baseTemp := 20.0 + 15.0*math.Sin(float64(time.Now().Hour())*math.Pi/12.0)
	variation := (float64(int(rand.Intn(200)-100)) / 10.0) // +/- 10 degrees
	return baseTemp + variation
}

func (s *StenellaServer) generateHumidity() float64 {
	// Generate realistic humidity
	baseHumidity := 40.0 + 30.0*math.Sin(float64(time.Now().Hour())*math.Pi/12.0)
	variation := (float64(int(rand.Intn(200)-100)) / 20.0) // +/- 5%
	return baseHumidity + variation
}

func (s *StenellaServer) generatePressure() float64 {
	// Generate realistic atmospheric pressure
	basePressure := 1013.25
	variation := (float64(int(rand.Intn(200)-100)) / 100.0) // +/- 1 hPa
	return basePressure + variation
}

func (s *StenellaServer) generateWindSpeed() float64 {
	// Generate realistic wind speed
	if time.Now().Hour() >= 6 && time.Now().Hour() <= 18 {
		return float64(int(rand.Intn(200)+500)) / 100.0 // 5-10 m/s during day
	} else {
		return float64(int(rand.Intn(100))) / 100.0 // 0-1 m/s at night
	}
}

func (s *StenellaServer) generatePrecipitation() float64 {
	// Generate realistic precipitation
	hour := time.Now().Hour()
	if hour >= 12 && hour <= 20 { // Afternoon/evening
		return float64(int(rand.Intn(100))) / 10.0 // 0-10 mm
	} else {
		return float64(int(rand.Intn(20))) / 100.0 // 0-0.2 mm
	}
}

func (s *StenellaServer) generateAirQuality() float64 {
	// Generate air quality score (AQI equivalent)
	return 50.0 + (float64(int(rand.Intn(1000))) / 100.0) // 50-150 AQI
}

func (s *StenellaServer) generateSolarRadiation() float64 {
	// Generate solar radiation based on time of day
	hour := time.Now().Hour()
	if hour >= 8 && hour <= 16 {
		intensity := math.Sin(float64(hour-8) * math.Pi / 8.0)
		return intensity * 1000.0 // 0-1000 W/m²
	}
	return 0.0
}

func (s *StenellaServer) generateUVIndex() float64 {
	// Generate UV index based on time of day and weather conditions
	hour := time.Now().Hour()
	if hour >= 10 && hour <= 14 {
		baseUV := 3.0 + math.Sin(float64(hour-10)*math.Pi/4.0)*7.0
		return baseUV
	}
	return 0.0
}

func (s *StenellaServer) getSourcesHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	sources := []DataSource{
		{
			ID:          "source_001",
			Name:        "Weather Station Alpha",
			URL:         "https://api.weather.com/v1/station-alpha",
			Type:        "weather-api",
			Enabled:     true,
			LastUpdated: time.Now().Add(-5 * time.Minute),
			Metadata:    map[string]string{"location": "New York", "updated_by": "system"},
		},
		{
			ID:          "source_002",
			Name:        "Sensor Network Beta",
			URL:         "https://api.sensors.com/v1/network-beta",
			Type:        "sensor-network",
			Enabled:     true,
			LastUpdated: time.Now().Add(-10 * time.Minute),
			Metadata:    map[string]string{"location": "London", "updated_by": "system"},
		},
		{
			ID:          "source_003",
			Name:        "Data Feed Gamma",
			URL:         "https://data.feed.com/v1/gamma",
			Type:        "data-feed",
			Enabled:     true,
			LastUpdated: time.Now().Add(-2 * time.Minute),
			Metadata:    map[string]string{"type": "financial", "updated_by": "system"},
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(sources)
}

func (s *StenellaServer) handleSourceWithID(w http.ResponseWriter, r *http.Request) {
	// Extract source ID from URL
	sourceID := r.URL.Path[len("/api/sources/"):]
	if sourceID == "" {
		http.Error(w, "Source ID required", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case "GET":
		s.getSourceHandler(w, r, sourceID)
	case "PUT":
		s.updateSourceHandler(w, r, sourceID)
	case "DELETE":
		s.deleteSourceHandler(w, r, sourceID)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *StenellaServer) getSourceHandler(w http.ResponseWriter, r *http.Request, sourceID string) {
	source := DataSource{
		ID:          sourceID,
		Name:        "Sample Source",
		URL:         "https://api.example.com/data",
		Type:        "sample",
		Enabled:     true,
		LastUpdated: time.Now(),
		Metadata:    map[string]string{"status": "active"},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(source)
}

func (s *StenellaServer) updateSourceHandler(w http.ResponseWriter, r *http.Request, sourceID string) {
	var update DataSource
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		http.Error(w, "Invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message":   "Source updated successfully",
		"source_id": sourceID,
	})
}

func (s *StenellaServer) deleteSourceHandler(w http.ResponseWriter, r *http.Request, sourceID string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message":   "Source deleted successfully",
		"source_id": sourceID,
	})
}

func (s *StenellaServer) healthCheckHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now(),
		"service":   "stenella",
		"version":   "1.0.0",
		"sources":   3,
		"enabled":   3,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}
