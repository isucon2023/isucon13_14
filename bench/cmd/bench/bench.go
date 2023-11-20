package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli"
	"go.uber.org/zap"

	"github.com/isucon/isucandar/agent"
	"github.com/isucon/isucandar/score"
	"github.com/isucon/isucon13/bench/internal/bencherror"
	"github.com/isucon/isucon13/bench/internal/benchscore"
	"github.com/isucon/isucon13/bench/internal/config"
	"github.com/isucon/isucon13/bench/internal/logger"
	"github.com/isucon/isucon13/bench/internal/resolver"
	"github.com/isucon/isucon13/bench/isupipe"
	"github.com/isucon/isucon13/bench/scenario"
)

var assetDir string

var enableSSL bool
var pretestOnly bool

type BenchResult struct {
	Pass     bool     `json:"pass"`
	Score    int64    `json:"score"`
	Messages []string `json:"messages"`
	Language string   `json:"language"`
}

// UniqueMsgs は重複除去したメッセージ配列を返します
func uniqueMsgs(msgs []string) (uniqMsgs []string) {
	dedup := map[string]struct{}{}
	for _, msg := range msgs {
		if _, ok := dedup[msg]; ok {
			continue
		}
		dedup[msg] = struct{}{}
		uniqMsgs = append(uniqMsgs, msg)
	}
	return
}

func dumpFailedResult(msgs []string) {
	lgr := zap.S()

	b, err := json.Marshal(&BenchResult{
		Pass:     false,
		Score:    0,
		Messages: msgs,
		Language: config.Language,
	})
	if err != nil {
		lgr.Warnf("失格判定結果書き出しに失敗. 運営に連絡してください: messages=%+v, err=%+v", msgs, err)
		fmt.Printf(`{"pass": false, "score": 0, "messages": ["%s"]}`, string(b))
		fmt.Println("")
		return
	}

	fmt.Println(string(b))
}

var run = cli.Command{
	Name:  "run",
	Usage: "ベンチマーク実行",
	Flags: []cli.Flag{
		cli.StringFlag{
			Name:        "target",
			Value:       fmt.Sprintf("http://pipe.u.isucon.dev:%d", config.TargetPort),
			Destination: &config.TargetBaseURL,
			EnvVar:      "BENCH_TARGET_URL",
		},
		cli.StringFlag{
			Name:        "nameserver",
			Value:       "127.0.0.1",
			Destination: &config.TargetNameserver,
			EnvVar:      "BENCH_NAMESERVER",
		},
		cli.IntFlag{
			Name:        "dns-port",
			Value:       53,
			Destination: &config.DNSPort,
			EnvVar:      "BENCH_DNS_PORT",
		},
		cli.StringFlag{
			Name:        "assetdir",
			Value:       "assets/testdata",
			Destination: &assetDir,
			EnvVar:      "BENCH_ASSETDIR",
		},
		cli.StringFlag{
			Name:        "staff-log-path",
			Destination: &config.StaffLogPath,
			EnvVar:      "BENCH_STAFF_LOG_PATH",
			Value:       "/tmp/staff.log",
		},
		cli.StringFlag{
			Name:        "contestant-log-path",
			Destination: &config.ContestantLogPath,
			EnvVar:      "BENCH_CONTESTANT_LOG_PATH",
			Value:       "/tmp/contestant.log",
		},
		cli.StringFlag{
			Name:        "result-path",
			Destination: &config.ResultPath,
			EnvVar:      "BENCH_RESULT_PATH",
			Value:       "/tmp/result.json",
		},
		cli.BoolFlag{
			Name:        "enable-ssl",
			Destination: &enableSSL,
			EnvVar:      "BENCH_ENABLE_SSL",
		},
		cli.BoolFlag{
			Name:        "pretest-only",
			Destination: &pretestOnly,
			EnvVar:      "BENCH_PRETEST_ONLY",
		},
	},
	Action: func(cliCtx *cli.Context) error {
		ctx := context.Background()
		lgr, err := logger.InitStaffLogger()
		if err != nil {
			return cli.NewExitError(err, 1)
		}

		contestantLogger, err := logger.InitContestantLogger()
		if err != nil {
			return cli.NewExitError(err, 1)
		}

		if enableSSL {
			config.HTTPScheme = "https"
			config.TargetPort = 443
			config.InsecureSkipVerify = false
			config.TargetBaseURL = fmt.Sprintf("%s://pipe.%s:%d", config.HTTPScheme, config.BaseDomain, config.TargetPort)
			contestantLogger.Info("SSL接続が有効になっています")
		} else {
			contestantLogger.Info("SSL接続が無効になっています")
		}

		lgr.Infof("webapp: %s", config.TargetBaseURL)
		lgr.Infof("nameserver: %s", net.JoinHostPort(config.TargetNameserver, strconv.Itoa(config.DNSPort)))

		// FIXME: アセット読み込み
		contestantLogger.Info("静的ファイルチェックを行います")
		contestantLogger.Info("静的ファイルチェックが完了しました")

		contestantLogger.Info("webappの初期化を行います")
		initClient, err := isupipe.NewClient(
			agent.WithBaseURL(config.TargetBaseURL),
			agent.WithTimeout(1*time.Minute),
		)
		if err != nil {
			dumpFailedResult([]string{"webapp初期化クライアント生成が失敗しました"})
			return cli.NewExitError(err, 1)
		}

		// FIXME: initialize以後のdumpFailedResult、ポータル報告への書き出しを実装
		// Actionsの結果にも乗ってしまうが、サイズ的に問題ないか
		// ベンチの出力変動が落ち着いてから実装する

		initializeResp, err := initClient.Initialize(ctx)
		if err != nil {
			dumpFailedResult([]string{"初期化が失敗しました"})
			return cli.NewExitError(fmt.Errorf("初期化が失敗しました: %w", err), 1)
		}
		config.Language = initializeResp.Language

		contestantLogger.Info("ベンチマーク走行前のデータ整合性チェックを行います")
		pretestDNSResolver := resolver.NewDNSResolver()
		pretestDNSResolver.ResolveAttempts = 10
		if err != nil {
			dumpFailedResult([]string{"整合性チェックDNSリゾルバ生成に失敗しました"})
			return cli.NewExitError(err, 1)
		}

		// pretest, benchmarkにはこれら初期化が必要
		benchscore.InitCounter(ctx)
		bencherror.InitErrors(ctx)
		if err := scenario.Pretest(ctx, pretestDNSResolver); err != nil {
			// FIXME: pretestのエラーを収集
			dumpFailedResult([]string{"整合性チェックに失敗しました"})
			return cli.NewExitError(err, 1)
		}
		contestantLogger.Info("整合性チェックが成功しました")

		if pretestOnly {
			lgr.Info("--pretest-onlyが指定されているため、ベンチマーク走行をスキップします")
			return nil
		}

		contestantLogger.Info("ベンチマーク走行を開始します")
		benchStartAt := time.Now()

		// pretest, benchmarkにはこれら初期化が必要
		benchscore.InitCounter(ctx)
		bencherror.InitErrors(ctx)

		benchCtx, cancelBench := context.WithTimeout(ctx, config.DefaultBenchmarkTimeout)
		defer cancelBench()

		benchmarker := newBenchmarker(benchCtx, contestantLogger)
		if err := benchmarker.run(benchCtx); err != nil {
			lgr.Warnf("ベンチマーク走行エラー: %s", err.Error())
			// FIXME: 失格相当エラーハンドリング
			dumpFailedResult([]string{})
		}

		benchElapsedSec := time.Now().Sub(benchStartAt)
		lgr.Infof("ベンチマーク走行時間: %s", benchElapsedSec.String())

		benchscore.DoneCounter()
		bencherror.Done()
		contestantLogger.Info("ベンチマーク走行終了")

		contestantLogger.Info("最終チェックを実施します")
		finalcheckDNSResolver := resolver.NewDNSResolver()
		finalcheckDNSResolver.ResolveAttempts = 10
		if err := scenario.FinalcheckScenario(ctx, finalcheckDNSResolver); err != nil {
			dumpFailedResult([]string{})
			return cli.NewExitError(err, 1)
		}
		contestantLogger.Info("最終チェックが成功しました")
		contestantLogger.Info("重複排除したログを以下に出力します")

		lgr.Info("===== ベンチ走行中エラー (重複排除済み) =====")
		var benchErrors []string
		for _, msgs := range bencherror.GetFinalErrorMessages() {
			benchErrors = append(benchErrors, msgs...)
		}
		benchErrors = uniqueMsgs(benchErrors)

		lgr.Info("===== ベンチ走行結果 =====")
		var msgs []string

		lgr.Info("シナリオカウンタ")
		scenarioCounter := benchmarker.ScenarioCounter()
		for name, count := range scenarioCounter {
			if strings.HasSuffix(string(name), "-fail") {
				lgr.Infof("[失敗シナリオ %s] %d 回失敗", name, count)
				continue
			}

			failKey := score.ScoreTag(fmt.Sprintf("%s-fail", name))
			if failCount, ok := scenarioCounter[failKey]; ok {
				lgr.Infof("[シナリオ %s] %d 回成功, %d 回失敗", name, count, failCount)
			} else {
				lgr.Infof("[シナリオ %s] %d 回実行", name, count)
			}
		}

		var (
			tooManySlows = benchscore.GetByTag(benchscore.TooSlow)
			tooManySpams = benchscore.GetByTag(benchscore.TooManySpam)
		)
		lgr.Infof("遅延離脱=%d, スパム離脱=%d", tooManySlows, tooManySpams)

		numResolves := benchscore.GetByTag(benchscore.DNSResolve)
		numDNSFailed := benchscore.GetByTag(benchscore.DNSFailed)
		msgs = append(msgs, fmt.Sprintf("名前解決成功数 %d", numResolves))
		lgr.Infof("名前解決成功数: %d", numResolves)
		lgr.Infof("名前解決失敗数: %d", numDNSFailed)

		profit := benchscore.GetTotalProfit()
		msgs = append(msgs, fmt.Sprintf("売上: %d", profit))
		lgr.Infof("スコア: %d", profit)

		b, err := json.Marshal(&BenchResult{
			Pass:     true,
			Score:    int64(profit),
			Messages: append(benchErrors, msgs...),
			Language: config.Language,
		})
		if err != nil {
			return cli.NewExitError(err, 1)
		}

		if err := os.WriteFile(config.ResultPath, b, os.ModePerm); err != nil {
			return cli.NewExitError(err, 1)
		}

		return nil
	},
}
