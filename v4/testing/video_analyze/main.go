package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"relay-x/v4/testing/video"
)

type Summary struct {
	Transport string `json:"transport"`
	Runs      int    `json:"runs"`

	SentFrames int64 `json:"sent_frames"`
	EchoFrames int64 `json:"echo_frames"`
	SentBytes  int64 `json:"sent_bytes"`
	EchoBytes  int64 `json:"echo_bytes"`

	LossFrames int64 `json:"loss_frames"`

	GoodputUpAvg   float64 `json:"goodput_mbps_up_avg"`
	GoodputUpMin   float64 `json:"goodput_mbps_up_min"`
	GoodputUpMax   float64 `json:"goodput_mbps_up_max"`
	GoodputDownAvg float64 `json:"goodput_mbps_down_avg"`
	GoodputDownMin float64 `json:"goodput_mbps_down_min"`
	GoodputDownMax float64 `json:"goodput_mbps_down_max"`

	RTTP50Avg float64 `json:"rtt_ms_p50_avg"`
	RTTP95Avg float64 `json:"rtt_ms_p95_avg"`
	RTTP99Avg float64 `json:"rtt_ms_p99_avg"`
	RTTMaxMax float64 `json:"rtt_ms_max_max"`

	WorstP95File string  `json:"worst_p95_file,omitempty"`
	WorstP95Ms   float64 `json:"worst_p95_ms,omitempty"`
}

func main() {
	out := flag.String("out", "", "输出 JSON 文件路径（可选）")
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Println("用法：video_analyze [-out out.json] report1.json report2.json ...")
		os.Exit(2)
	}

	var all []record
	for _, p := range flag.Args() {
		reports, err := readReports(p)
		if err != nil {
			fmt.Printf("读取失败 %s: %v\n", p, err)
			os.Exit(1)
		}
		for _, r := range reports {
			all = append(all, record{file: p, report: r})
		}
	}

	byTransport := map[string][]record{}
	for _, r := range all {
		byTransport[r.report.Transport] = append(byTransport[r.report.Transport], r)
	}

	var keys []string
	for k := range byTransport {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var summaries []Summary
	for _, k := range keys {
		summaries = append(summaries, summarize(k, byTransport[k]))
	}

	raw, _ := json.MarshalIndent(summaries, "", "  ")
	fmt.Println(string(raw))
	if *out != "" {
		_ = os.WriteFile(*out, raw, 0o644)
	}
}

type record struct {
	file   string
	report video.Report
}

func readReports(path string) ([]video.Report, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rs []video.Report
	if err := json.Unmarshal(b, &rs); err == nil && len(rs) > 0 {
		return rs, nil
	}
	var r video.Report
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return []video.Report{r}, nil
}

func summarize(transport string, rs []record) Summary {
	s := Summary{
		Transport: transport,
		Runs:      len(rs),
	}
	if len(rs) == 0 {
		return s
	}

	s.GoodputUpMin = rs[0].report.GoodputMbpsUp
	s.GoodputUpMax = rs[0].report.GoodputMbpsUp
	s.GoodputDownMin = rs[0].report.GoodputMbpsDown
	s.GoodputDownMax = rs[0].report.GoodputMbpsDown

	var worstP95 float64
	var worstFile string

	for _, r := range rs {
		s.SentFrames += r.report.SentFrames
		s.EchoFrames += r.report.EchoFrames
		s.SentBytes += r.report.SentBytes
		s.EchoBytes += r.report.EchoBytes

		if r.report.GoodputMbpsUp < s.GoodputUpMin {
			s.GoodputUpMin = r.report.GoodputMbpsUp
		}
		if r.report.GoodputMbpsUp > s.GoodputUpMax {
			s.GoodputUpMax = r.report.GoodputMbpsUp
		}
		if r.report.GoodputMbpsDown < s.GoodputDownMin {
			s.GoodputDownMin = r.report.GoodputMbpsDown
		}
		if r.report.GoodputMbpsDown > s.GoodputDownMax {
			s.GoodputDownMax = r.report.GoodputMbpsDown
		}

		s.GoodputUpAvg += r.report.GoodputMbpsUp
		s.GoodputDownAvg += r.report.GoodputMbpsDown

		s.RTTP50Avg += r.report.RTTMsP50
		s.RTTP95Avg += r.report.RTTMsP95
		s.RTTP99Avg += r.report.RTTMsP99
		if r.report.RTTMsMax > s.RTTMaxMax {
			s.RTTMaxMax = r.report.RTTMsMax
		}

		if r.report.RTTMsP95 >= worstP95 {
			worstP95 = r.report.RTTMsP95
			worstFile = r.file
		}
	}

	s.LossFrames = s.SentFrames - s.EchoFrames
	s.GoodputUpAvg /= float64(len(rs))
	s.GoodputDownAvg /= float64(len(rs))
	s.RTTP50Avg /= float64(len(rs))
	s.RTTP95Avg /= float64(len(rs))
	s.RTTP99Avg /= float64(len(rs))
	s.WorstP95File = worstFile
	s.WorstP95Ms = worstP95
	return s
}
