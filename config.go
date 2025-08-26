package bakemono

import (
	"errors"
	"fmt"
	"os"

	"go.uber.org/zap"
	"gopkg.in/yaml.v2"
)

var (
	ascii = `
___ _ _  _ _   _ ___  ____ _    ____ _  _ ____ ____ ____ 
 |  | |\ |  \_/  |__] |__| |    |__| |\ | |    |___ |__/ 
 |  | | \|   |   |__] |  | |___ |  | | \| |___ |___ |  \                                        
`
)

// Config configuration details of balancer
type Config struct {
	// log config
	AccessLogPath string     `yaml:"access_log_path"`
	ZapConfig     zap.Config `yaml:"zap_config"`

	// cache config
	Path         string `yaml:"path"`
	SizeMb       uint64 `yaml:"size_mb"`
	AvgChunkSize uint64 `yaml:"avg_chunk_size"`

	SSLCertificateKey string `yaml:"ssl_certificate_key"`
	Location          []struct {
		Pattern string `yaml:"pattern"`
	} `yaml:"location"`
	Upstream struct {
		ProxyPass   []string `yaml:"proxy_pass"`
		BalanceMode string   `yaml:"balance_mode"`
	} `yaml:"upstream"`
	Schema              string `yaml:"schema"`
	Port                int    `yaml:"port"`
	SSLCertificate      string `yaml:"ssl_certificate"`
	HealthCheck         bool   `yaml:"tcp_health_check"`
	HealthCheckInterval uint   `yaml:"health_check_interval"`
	MaxAllowed          uint   `yaml:"max_allowed"`
}

// ReadConfig read configuration from `fileName` file
func ReadConfig(fileName string) (*Config, error) {
	in, err := os.ReadFile(fileName)
	if err != nil {
		return nil, err
	}
	var config Config
	err = yaml.Unmarshal(in, &config)
	if err != nil {
		return nil, err
	}
	return &config, nil
}

// Print print config details
func (c *Config) Print() {
	fmt.Printf("%s\nSchema: %s\nPort: %d\nHealth Check: %v\nLocation:\n",
		ascii, c.Schema, c.Port, c.HealthCheck)
	for _, l := range c.Location {
		fmt.Printf("\tRoute: %s\n", l.Pattern)
	}
	fmt.Printf("Proxy Pass: %s\nMode: %s\n", c.Upstream.ProxyPass, c.Upstream.BalanceMode)
}

// Validation verify the configuration details of the balancer
func (c *Config) Validation() error {
	if c.Schema != "http" && c.Schema != "https" {
		return fmt.Errorf("the schema \"%s\" not supported", c.Schema)
	}
	if len(c.Location) == 0 {
		return errors.New("the details of location cannot be null")
	}
	if c.Schema == "https" && (len(c.SSLCertificate) == 0 || len(c.SSLCertificateKey) == 0) {
		return errors.New("the https proxy requires ssl_certificate_key and ssl_certificate")
	}
	if c.HealthCheckInterval < 1 {
		return errors.New("health_check_interval must be greater than 0")
	}
	return nil
}
