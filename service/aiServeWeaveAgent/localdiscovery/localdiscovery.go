// Package localdiscovery preserves the Agent discovery API shared with Gateway.
//
// Package localdiscovery 保留 Agent 发现 API，实现与 Gateway 共享。
package localdiscovery

import "AIServeWeave/common/localdiscovery"

// DefaultInterval is the interval between local scans. / DefaultInterval 是本机扫描间隔。
const DefaultInterval = localdiscovery.DefaultInterval

// Candidate is one local backend to probe. / Candidate 是待探测的本机后端。
type Candidate = localdiscovery.Candidate

// Config configures local discovery. / Config 配置本机发现。
type Config = localdiscovery.Config

// Scanner discovers local backends. / Scanner 发现本机后端。
type Scanner = localdiscovery.Scanner

// DefaultCandidates returns well-known loopback endpoints.
// DefaultCandidates 返回已知的回环端点。
func DefaultCandidates() []Candidate { return localdiscovery.DefaultCandidates() }

// New constructs a local scanner. / New 构造本机扫描器。
func New(cfg Config) (*Scanner, error) { return localdiscovery.New(cfg) }
