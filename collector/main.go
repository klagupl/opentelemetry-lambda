// Copyright The OpenTelemetry Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsConfig "github.com/aws/aws-sdk-go-v2/config"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/open-telemetry/opentelemetry-lambda/collector/extension"
	"github.com/open-telemetry/opentelemetry-lambda/collector/lambdacomponents"
	"go.uber.org/zap"
)

var (
	extensionName   = filepath.Base(os.Args[0]) // extension name has to match the filename
	extensionClient = extension.NewClient(os.Getenv("AWS_LAMBDA_RUNTIME_API"))
	logger          = zap.NewExample()
	ssmClient       = &ssm.Client{}
)

func main() {
	logger.Debug("Launching OpenTelemetry Lambda extension", zap.String("version", Version))
	cfg, err := awsConfig.LoadDefaultConfig(context.Background())
	if err != nil {
		panic("configuration error, " + err.Error())
	}
	ssmClient = ssm.NewFromConfig(cfg)

	factories, _ := lambdacomponents.Components()
	config, err := getSsmConfig()
	if err != nil {
		logger.Error("%s", zap.Field{String: err.Error()})
		config = getConfig()
	}
	collector := NewCollector(factories, config)
	ctx, cancel := context.WithCancel(context.Background())

	if err := collector.Start(ctx); err != nil {
		log.Fatalf("Failed to start the extension: %v", err)
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigs
		cancel()
		logger.Debug(fmt.Sprintf("Received", s))
		logger.Debug("Exiting")
	}()

	res, err := extensionClient.Register(ctx, extensionName)
	if err != nil {
		log.Fatalf("Cannot register extension: %v", err)
	}

	logger.Debug("Register ", zap.String("response :", prettyPrint(res)))
	// Will block until shutdown event is received or cancelled via the context.
	processEvents(ctx, collector)
}

func getSsmConfig() (string, error) {
	output, err := ssmClient.GetParameter(context.Background(), &ssm.GetParameterInput{
		Name: aws.String(os.Getenv("OPENTELEMETRY_SSM_PARAMETER_NAME")),
	})
	if err != nil {
		return "", err
	}
	path := "/tmp/" + "ssm_collector.yml"
	file, err := os.Create(path)
	if err != nil {
		return "", err
	}
	_, err = file.WriteString(*output.Parameter.Value)
	if err != nil {
		return "", err
	}
	return path, nil
}

func getConfig() string {

	val, ex := os.LookupEnv("OPENTELEMETRY_COLLECTOR_CONFIG_FILE")
	if !ex {
		return "/opt/collector-config/config.yaml"
	}
	log.Printf("Using config file at path %v", val)
	return val
}

func processEvents(ctx context.Context, collector *Collector) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			logger.Debug("Waiting for event...")
			res, err := extensionClient.NextEvent(ctx)
			if err != nil {
				logln("Error:", err)
				logln("Exiting")
				return
			}

			logger.Debug("Received ", zap.String("event :", prettyPrint(res)))
			// Exit if we receive a SHUTDOWN event
			if res.EventType == extension.Shutdown {
				collector.Stop() // TODO: handle return values
				logger.Debug("Received SHUTDOWN event")
				logger.Debug("Exiting")
				return
			}
		}
	}
}
