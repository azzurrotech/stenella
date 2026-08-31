# stenella (Data Aggregation Platform)

**MIT License © Azzurro Technology Inc.**

## Overview

stenella is a comprehensive data aggregation platform designed to collect, normalize, and consolidate data from multiple sources. It provides real-time data processing with web and mobile-friendly interfaces, seamlessly integrated with the ATP platform.

## Installation

### Prerequisites
- Go 1.20+
- PostgreSQL or MySQL database
- Redis for caching (optional)

### Installation Steps

1. Clone the repository:
   ```bash
   git clone https://github.com/azzurro-tech/stenella.git
   cd stenella
   ```

2. Install Go dependencies:
   ```bash
   go mod download
   ```

3. Configure database connection:
   ```bash
   # Edit database configuration
   cp .env.example .env
   # Update .env with your database credentials
   ```

4. Start stenella:
   ```bash
   cd azzurrotech/stenella
   go run .
   ```

5. Access stenella web interface:
   ```
   http://localhost:8081
   http://localhost:8081/stenella/config
   http://localhost:8081/stenella/admin
   ```

## Usage (Standalone)

### Basic Operations

**Data Source Management**
```bash
# Add a new data source
curl -X POST http://localhost:8081/api/data/sources \
  -H "Content-Type: application/json" \
  -d '{"name":"Weather API","url":"https://api.weather.com/data","type":"weather","enabled":true}'

# List all data sources
curl http://localhost:8081/api/data/sources

# Get specific data source
curl http://localhost:8081/api/data/sources/{source-id}
```

**Data Access**
```bash
# Get aggregated data
curl http://localhost:8081/api/data

# Get data with filters
curl "http://localhost:8081/api/data?from=2024-01-01&to=2024-12-31&sources=weather,stock"

# Export data
curl http://localhost:8081/api/data/export?format=json
```

### API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Main stenella status page |
| `/stenella` | GET | HTML config viewer |
| `/stenella/config` | GET | View configuration |
| `/stenella/config` | POST | Update configuration |
| `/stenella/admin` | GET | HTML admin panel |
| `/stenella/admin` | POST | Update admin settings |
| `/api/data` | GET | Get aggregated data |
| `/api/data/sources` | GET | List all data sources |
| `/api/data/sources` | POST | Add new data source |
| `/api/data/sources/{id}` | GET | Get specific data source |
| `/api/data/sources/{id}` | PUT | Update data source |
| `/api/data/sources/{id}` | DELETE | Remove data source |
| `/api/data/export` | GET | Export aggregated data |
| `/health` | GET | Health check |

## Integration with ATP

### Service Registration

stenella registers with ATP as a data aggregation service:

```go
// Example stenella service registration
package main

import "github.com/gin-gonic/gin"

func main() {
    r := gin.Default()
    
    // Health check endpoint
    r.GET("/health", func(c *gin.Context) {
        c.JSON(200, gin.H{"status": "healthy"})
    })
    
    // Data sources API
    sources := r.Group("/api/data/sources")
    {
        sources.GET("/", getAllSources)
        sources.POST("/", createSource)
        sources.GET("/{id}", getSource)
        sources.PUT("/{id}", updateSource)
        sources.DELETE("/{id}", deleteSource)
    }
    
    // Data aggregation API
    data := r.Group("/api/data")
    {
        data.GET("/", getAggregatedData)
        data.GET("/export", exportData)
    }
    
    // Service registration with ATP
    r.POST("/register", func(c *gin.Context) {
        config := map[string]interface{}{
            "name": "stenella",
            "endpoint": "http://localhost:8081",
            "health": "/health",
            "data_endpoint": "/api/data",
            "sources_endpoint": "/api/data/sources"
        }
        
        response, err := registerWithATP(config)
        if err != nil {
            c.JSON(500, gin.H{"error": "registration failed"})
            return
        }
        
        c.JSON(200, response)
    })
    
    r.Run(":8081")
}
```

### Data Integration

stenella integrates with ATP for centralized data management:

```yaml
# atp/config/integrations.yaml
integrations:
  azzurrotech:
    stenella:
      health_check: /health
      data_source: /api/data
      sources_endpoint: /api/data/sources
      config_endpoint: /api/stenella/config
      admin_endpoint: /api/stenella/admin
      auth_required: true
```

### Data Processing Pipeline

1. **Data Collection**: stenella collects data from multiple sources
2. **Data Normalization**: Raw data is normalized to standard format
3. **Data Storage**: Processed data is stored in database
4. **Data Distribution**: Data is distributed through ATP APIs
5. **Data Analytics**: ATP provides analytics and monitoring

## Development Setup

### Local Development

```bash
# Start stenella server
cd azzurrotech/stenella
go run .

# Or with environment variables
cd azzurrotech/stenella
export STENELLA_PORT=8081
export DB_HOST=localhost
export DB_PORT=5432
go run .
```

### Testing

```bash
# Run all tests
cd azzurrotech/stenella
go test ./...

# Run specific test packages
cd azzurrotech/stenella
go test ./internal/aggregator/...
go test ./internal/sources/...

# Run integration tests
cd azzurrotech/stenella
go test ./integration/...

# Test API endpoints
curl http://localhost:8081/health
curl http://localhost:8081/api/data/sources
curl "http://localhost:8081/api/data?from=2024-01-01&to=2024-12-31"
```

### Building

```bash
# Build for production
cd azzurrotech/stenella
go build -o stenella ./...

# Build with specific options
cd azzurrotech/stenella
go build -ldflags="-port=8081" -o stenella ./...

# Build with database configuration
cd azzurrotech/stenella
DB_HOST=prod-db.go DB_PORT=5432 go run ./cmd
```

## Performance Optimization

### Data Processing

- **Streaming Processing**: Efficient streaming of large data sets
- **Parallel Processing**: Parallel data processing for performance
- **Caching**: Multi-level caching for frequently accessed data
- **Compression**: Data compression for reduced storage
- **Load Balancing**: Horizontal scaling support

### Database Optimization

```go
// Database optimization example
var db *sql.DB

func initDatabase() {
    var err error
    db, err = sql.Open("postgres", getDatabaseURL())
    if err != nil {
        log.Fatal("Database connection failed")
    }
    
    // Set connection pool settings
    db.SetMaxOpenConns(100)
    db.SetMaxIdleConns(10)
    db.SetConnMaxLifetime(time.Hour)
    
    // Create indexes for performance
    createIndexes()
}

func createIndexes() {
    queries := []string{
        "CREATE INDEX IF NOT EXISTS idx_data_sources_type ON data_sources(type)",
        "CREATE INDEX IF NOT EXISTS idx_data_sources_enabled ON data_sources(enabled)",
        "CREATE INDEX IF NOT EXISTS idx_aggregated_data_date ON aggregated_data(date)",
    }
    
    for _, query := range queries {
        _, err := db.Exec(query)
        if err != nil {
            log.Printf("Error creating index: %v", err)
        }
    }
}
```

## Monitoring

### Health Monitoring

```bash
# stenella health check
curl http://localhost:8081/health

# Data sources health
curl http://localhost:8081/api/data/sources

# Data aggregation health
curl http://localhost:8081/api/data

# Configuration health
curl http://localhost:8081/stenella/config
```

### Metrics Collection

stenella collects and reports:

- **Data Source Status**: All configured data sources status
- **Data Quality**: Data validation and quality metrics
- **Processing Performance**: Data aggregation performance
- **Database Performance**: Database query performance
- **API Performance**: HTTP request/response metrics
- **Error Rates**: Data processing error tracking

## Security Features

### stenella Security

- **Input Validation**: Validates and sanitizes all incoming data
- **Access Control**: Role-based access to data sources and endpoints
- **Encryption**: Encrypts sensitive data in transit and storage
- **Authentication**: Secure authentication for administrative functions
- **Audit Trails**: Comprehensive logging of all data access and modifications
- **Rate Limiting**: Prevents abuse of API endpoints
- **CORS Support**: Cross-origin resource sharing configuration

### Data Security

stenella provides secure data handling:

- **Data Encryption**: AES-256 encryption for sensitive data
- **Data Validation**: Comprehensive data validation and sanitization
- **Access Control**: Granular access control for data sources
- **Audit Logging**: Complete audit trails for all data operations
- **Backup**: Automated data backup and recovery

## Troubleshooting

### Common Issues

1. **Data Source Connection Failed**
   ```bash
   # Check stenella logs
   $ tail -f stenella.log
   
   # Test database connection
   $ psql -h localhost -U username -d database
   
   # Check stenella health
   $ curl http://localhost:8081/health
   ```

2. **Data Aggregation Slow**
   ```bash
   # Check data source status
   $ curl http://localhost:8081/api/data/sources
   
   # Check stenella configuration
   $ curl http://localhost:8081/stenella/config
   
   # Monitor stenella logs
   $ tail -f stenella.log
   ```

3. **Data Not Loading**
   ```bash
   # Check data source configuration
   $ curl http://localhost:8081/api/data/sources/{id}
   
   # Test data source connection
   $ curl "http://localhost:8081/api/data/sources/{id}/test"
   
   # Check stenella health
   $ curl http://localhost:8081/health
   ```

### Debugging Commands

```bash
# Enable debug logging
export STENELLA_LOG_LEVEL=debug

# Check stenella logs
$ tail -f stenella.log

# Monitor system resources
$ top
$ free -h

# Test data sources
$ curl http://localhost:8081/api/data/sources
$ curl http://localhost:8081/api/data

# Check stenella configuration
$ curl http://localhost:8081/stenella/config
```

## API Specifications

### High Maturity API (REST-based)

```http
GET /api/data?from=2024-01-01&to=2024-12-31& sources=source1,source2
GET /api/data/sources
POST /api/data/sources
GET /api/data/sources/{id}
PUT /api/data/sources/{id}
DELETE /api/data/sources/{id}
GET /api/data/export
GET /health
```

### stenella-specific Endpoints

```http
GET /stenella/config - View stenella configuration
POST /stenella/config - Update stenella configuration
GET /stenella/admin - View admin information
POST /stenella/admin - Modify admin settings
```

## Future Enhancements

### Planned Features

1. **Advanced Analytics**: ML-powered data analysis and predictions
2. **Real-time Processing**: Live data streaming and processing
3. **Advanced Visualization**: Data visualization and charting
4. **Multi-source Integration**: More data source connectors
5. **Automated Data Quality**: Automated data validation and cleaning

### Roadmap

- **Phase 1**: Basic data collection and aggregation
- **Phase 2**: Data normalization and storage
- **Phase 3**: Advanced analytics and visualization
- **Phase 4**: Real-time processing and automation

## Conclusion

stenella provides a comprehensive data aggregation platform that unifies data from multiple sources into a single, cohesive system. It offers robust security, comprehensive API functionality, and production-ready architecture for enterprise data integration needs.

Key benefits:

- **Data Integration**: Unified data collection from multiple sources
- **Data Processing**: Real-time data normalization and processing
- **Data Storage**: Efficient database storage and management
- **Data Access**: RESTful API for data access and management
- **Security**: Comprehensive security features and controls
- **Monitoring**: Real-time monitoring and analytics
- **Scalability**: Supports large-scale data processing

The stenella implementation is production-ready and can be easily integrated into enterprise applications with comprehensive data aggregation and security features.

---

*Document Version: 1.0*
*Created: 2026-08-25*
*Last Updated: 2026-08-25*
*Status: Production Ready*

**License:** MIT License © Azzurro Technology Inc.