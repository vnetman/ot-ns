// Copyright (c) 2020, The OTNS Authors.
// All rights reserved.
//
// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are met:
// 1. Redistributions of source code must retain the above copyright
//    notice, this list of conditions and the following disclaimer.
// 2. Redistributions in binary form must reproduce the above copyright
//    notice, this list of conditions and the following disclaimer in the
//    documentation and/or other materials provided with the distribution.
// 3. Neither the name of the copyright holder nor the
//    names of its contributors may be used to endorse or promote products
//    derived from this software without specific prior written permission.
//
// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
// AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
// IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE
// ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE
// LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR
// CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF
// SUBSTITUTE GOODS OR SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS
// INTERRUPTION) HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN
// CONTRACT, STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE)
// ARISING IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
// POSSIBILITY OF SUCH DAMAGE.

package otns_main

import (
	"flag"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vnetman/ot-ns/cli/runcli"

	"github.com/vnetman/ot-ns/threadconst"

	"github.com/vnetman/ot-ns/dispatcher"

	webSite "github.com/vnetman/ot-ns/web/site"

	"github.com/vnetman/ot-ns/web"

	"github.com/pkg/errors"

	"github.com/vnetman/ot-ns/progctx"
	"github.com/vnetman/ot-ns/visualize"

	visualizeGrpc "github.com/vnetman/ot-ns/visualize/grpc"

	visualizeMulti "github.com/vnetman/ot-ns/visualize/multi"

	"github.com/vnetman/ot-ns/cli"

	"github.com/vnetman/ot-ns/simulation"
	"github.com/simonlingoogle/go-simplelogger"
)

type MainArgs struct {
	Speed          string
	OtCliPath      string
	AutoGo         bool
	ReadOnly       bool
	LogLevel       string
	OpenWeb        bool
	RawMode        bool
	Real           bool
	ListenAddr     string
	DispatcherHost string
	DispatcherPort int
	DumpPackets    bool
	NoPcap         bool
	NoReplay       bool
}

var (
	args MainArgs
)

func parseArgs() {
	defaultOtCli := os.Getenv("OTNS_OT_CLI")
	if defaultOtCli == "" {
		defaultOtCli = "./ot-cli-ftd"
	}

	flag.StringVar(&args.Speed, "speed", "1", "set simulating speed")
	flag.StringVar(&args.OtCliPath, "ot-cli", defaultOtCli, "specify the OT CLI executable")
	flag.BoolVar(&args.AutoGo, "autogo", true, "auto go")
	flag.BoolVar(&args.ReadOnly, "readonly", false, "readonly simulation can not be manipulated")
	flag.StringVar(&args.LogLevel, "log", "warn", "set logging level")
	flag.BoolVar(&args.OpenWeb, "web", true, "open web")
	flag.BoolVar(&args.RawMode, "raw", false, "use raw mode")
	flag.BoolVar(&args.Real, "real", false, "use real mode (for real devices)")
	flag.StringVar(&args.ListenAddr, "listen", fmt.Sprintf("localhost:%d", threadconst.InitialDispatcherPort), "specify listen address")
	flag.BoolVar(&args.DumpPackets, "dump-packets", false, "dump packets")
	flag.BoolVar(&args.NoPcap, "no-pcap", false, "do not generate Pcap")
	flag.BoolVar(&args.NoReplay, "no-replay", false, "do not generate Replay")

	flag.Parse()
}

func parseListenAddr() {
	var err error

	notifyInvalidListenAddr := func() {
		simplelogger.Fatalf("invalid listen address: %s (port must be larger than or equal to 9000 and must be a multiple of 1000).", args.ListenAddr)
	}

	subs := strings.Split(args.ListenAddr, ":")
	if len(subs) != 2 {
		notifyInvalidListenAddr()
	}

	args.DispatcherHost = subs[0]
	if args.DispatcherPort, err = strconv.Atoi(subs[1]); err != nil {
		notifyInvalidListenAddr()
	}

	if args.DispatcherPort < threadconst.InitialDispatcherPort || args.DispatcherPort%threadconst.WellKnownNodeId != 0 {
		notifyInvalidListenAddr()
	}

	portOffset := (args.DispatcherPort - threadconst.InitialDispatcherPort) / threadconst.WellKnownNodeId
	simplelogger.Infof("Using env PORT_OFFSET=%d", portOffset)
	if err = os.Setenv("PORT_OFFSET", strconv.Itoa(portOffset)); err != nil {
		simplelogger.Panic(err)
	}
}

func Main(ctx *progctx.ProgCtx, visualizerCreator func(ctx *progctx.ProgCtx, args *MainArgs) visualize.Visualizer, cliOptions *runcli.CliOptions) {
	parseArgs()

	simplelogger.SetLevel(simplelogger.ParseLevel(args.LogLevel))

	parseListenAddr()

	rand.Seed(time.Now().UnixNano())
	// run console in the main goroutine
	ctx.Defer(func() {
		_ = os.Stdin.Close()
	})

	handleSignals(ctx)

	var vis visualize.Visualizer
	if visualizerCreator != nil {
		vis = visualizerCreator(ctx, &args)
	}

	visGrpcServerAddr := fmt.Sprintf("%s:%d", args.DispatcherHost, args.DispatcherPort-1)

	replayFn := ""
	if !args.NoReplay {
		replayFn = fmt.Sprintf("otns_%s.replay", os.Getenv("PORT_OFFSET"))
	}
	if vis != nil {
		vis = visualizeMulti.NewMultiVisualizer(
			vis,
			visualizeGrpc.NewGrpcVisualizer(visGrpcServerAddr, replayFn),
		)
	} else {
		vis = visualizeGrpc.NewGrpcVisualizer(visGrpcServerAddr, replayFn)
	}

	sim := createSimulation(ctx)
	rt := cli.NewCmdRunner(ctx, sim)
	sim.SetVisualizer(vis)
	go sim.Run()
	go func() {
		err := cli.Run(rt, cliOptions)
		ctx.Cancel(errors.Wrapf(err, "console exit"))
	}()

	go func() {
		siteAddr := fmt.Sprintf("%s:%d", args.DispatcherHost, args.DispatcherPort-3)
		err := webSite.Serve(siteAddr)
		if err != nil {
			simplelogger.Errorf("site quited: %+v, OTNS-Web won't be available!", err)
		}
	}()

	if args.AutoGo {
		go autoGo(ctx, sim)
	}

	web.ConfigWeb(args.DispatcherHost, args.DispatcherPort-2, args.DispatcherPort-1, args.DispatcherPort-3)

	simplelogger.Debugf("open web: %v", args.OpenWeb)
	if args.OpenWeb {
		_ = web.OpenWeb(ctx)
	}

	vis.Run() // visualize must run in the main thread

	simplelogger.Infof("waiting for OTNS to stop gracefully ...")
	ctx.Wait()
}

func handleSignals(ctx *progctx.ProgCtx) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGHUP)
	signal.Ignore(syscall.SIGALRM)

	ctx.WaitAdd("handleSignals", 1)
	go func() {
		defer ctx.WaitDone("handleSignals")
		defer simplelogger.Debugf("handleSignals exit.")

		for {
			select {
			case sig := <-c:
				simplelogger.Infof("signal received: %v", sig)
				ctx.Cancel(nil)
			case <-ctx.Done():
				return
			}
		}
	}()
}

func autoGo(prog *progctx.ProgCtx, sim *simulation.Simulation) {
	for {
		<-sim.Go(time.Second)
	}
}

func createSimulation(ctx *progctx.ProgCtx) *simulation.Simulation {
	var speed float64
	var err error

	simcfg := simulation.DefaultConfig()
	simcfg.OtCliPath = args.OtCliPath

	args.Speed = strings.ToLower(args.Speed)
	if args.Speed == "max" {
		speed = dispatcher.MaxSimulateSpeed
	} else {
		speed, err = strconv.ParseFloat(args.Speed, 64)
		simplelogger.PanicIfError(err)
	}
	simcfg.Speed = speed
	simcfg.ReadOnly = args.ReadOnly
	simcfg.RawMode = args.RawMode
	simcfg.Real = args.Real
	simcfg.DispatcherHost = args.DispatcherHost
	simcfg.DispatcherPort = args.DispatcherPort
	simcfg.DumpPackets = args.DumpPackets

	dispatcherCfg := dispatcher.DefaultConfig()
	dispatcherCfg.NoPcap = args.NoPcap

	sim, err := simulation.NewSimulation(ctx, simcfg, dispatcherCfg)
	simplelogger.FatalIfError(err)
	return sim
}
