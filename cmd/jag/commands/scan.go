// Copyright (C) 2021 Toitware ApS. All rights reserved.
// Use of this source code is governed by an MIT-style license that can be
// found in the LICENSE file.

package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"time"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"github.com/toitlang/jaguar/cmd/jag/directory"
)

const (
	scanTimeout = 600 * time.Millisecond
	scanPort    = 1990
)

func ScanCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan for Jaguar devices",
		Long: "Scan for Jaguar devices by listening for UDP packets broadcasted by the devices.\n" +
			"To use a Jaguar device, you need to be on the same network as the device.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, err := directory.GetWorkspaceConfig()
			if err != nil {
				return err
			}

			port, err := cmd.Flags().GetUint("port")
			if err != nil {
				return err
			}

			timeout, err := cmd.Flags().GetDuration("timeout")
			if err != nil {
				return err
			}

			outputter, err := parseOutputFlag(cmd)
			if err != nil {
				return err
			}

			cmd.SilenceUsage = true
			if outputter != nil {
				scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
				devices, err := scan(scanCtx, port)
				cancel()
				if err != nil {
					return err
				}
				return outputter.Encode(Devices{devices})
			}

			device, _, err := scanAndPickDevice(ctx, timeout, port, nil, false)
			if err != nil {
				return err
			}
			cfg.Set("device", device)
			return cfg.WriteConfig()
		},
	}

	cmd.Flags().BoolP("list", "l", false, "If set, list the devices")
	cmd.Flags().StringP("output", "o", "short", "Set output format to json, yaml or short (works only with '--list')")
	cmd.Flags().UintP("port", "p", scanPort, "UDP port to scan for devices on")
	cmd.Flags().DurationP("timeout", "t", scanTimeout, "how long to scan")
	return cmd
}

type deviceSelect interface {
	Match(d Device) bool
}

type deviceIDSelect string

func (s deviceIDSelect) Match(d Device) bool {
	return string(s) == d.ID
}

func (s deviceIDSelect) String() string {
	return fmt.Sprintf("device with ID: '%s'", string(s))
}

type deviceNameSelect string

func (s deviceNameSelect) Match(d Device) bool {
	return string(s) == d.Name
}

func (s deviceNameSelect) String() string {
	return fmt.Sprintf("device with name: '%s'", string(s))
}

func scanAndPickDevice(ctx context.Context, scanTimeout time.Duration, port uint, autoSelect deviceSelect, manualPick bool) (*Device, bool, error) {
	fmt.Println("Scanning ...")
	scanCtx, cancel := context.WithTimeout(ctx, scanTimeout)
	devices, err := scan(scanCtx, port)
	cancel()
	if err != nil {
		return nil, false, err
	}

	if len(devices) == 0 {
		return nil, false, fmt.Errorf("didn't find any Jaguar devices")
	}
	if autoSelect != nil {
		for _, d := range devices {
			if autoSelect.Match(d) {
				return &d, true, nil
			}
		}
		if manualPick {
			return nil, false, fmt.Errorf("couldn't find %s", autoSelect)
		}
	}

	prompt := promptui.Select{
		Label:     "Choose what Jaguar device you want to use",
		Items:     devices,
		Templates: &promptui.SelectTemplates{},
	}

	i, _, err := prompt.Run()
	if err != nil {
		return nil, false, fmt.Errorf("you didn't select anything")
	}

	res := devices[i]
	return &res, false, nil
}

func scan(ctx context.Context, port uint) ([]Device, error) {
	pc, err := net.ListenPacket("udp4", fmt.Sprintf(":%d", port))
	if err != nil {
		return nil, err
	}
	defer pc.Close()
	if deadline, ok := ctx.Deadline(); ok {
		if err := pc.SetDeadline(deadline); err != nil {
			return nil, err
		}
	}

	devices := map[string]Device{}
looping:
	for {
		select {
		case <-ctx.Done():
			err := ctx.Err()
			if err == context.DeadlineExceeded {
				break looping
			}
			return nil, err
		default:
		}

		buf := make([]byte, 1024)
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			if isTimeoutError(err) {
				break looping
			}
			return nil, err
		}

		dev, err := parseDevice(buf[:n])
		if err != nil {
			fmt.Println("Failed to parse identify", err)
		} else if dev != nil {
			devices[dev.Address] = *dev
		}
	}

	var res []Device
	for _, d := range devices {
		res = append(res, d)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

type udpMessage struct {
	Method  string          `json:"method"`
	Payload json.RawMessage `json:"payload"`
}

func parseDevice(b []byte) (*Device, error) {
	var res Device

	var msg udpMessage
	if err := json.Unmarshal(b, &msg); err != nil {
		return nil, fmt.Errorf("could not parse message: %s. Reason: %w", string(b), err)
	}
	if msg.Method != "jaguar.identify" {
		return nil, nil
	}

	if err := json.Unmarshal(msg.Payload, &res); err != nil {
		return nil, fmt.Errorf("failed to parse payload of jaguar.identify: %s. reason: %w", string(b), err)
	}
	return &res, nil
}

func isTimeoutError(err error) bool {
	e, ok := err.(net.Error)
	return ok && e.Timeout()
}
