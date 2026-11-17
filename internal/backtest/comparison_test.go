package backtest_test

import (
 "testing"
 "time"
 "github.com/saintmili/billionaire/internal/backtest"
)

func TestComparisonExposesOverlapWithoutClaimingMatchedPerformance(t *testing.T) {
 at:=time.Date(2026,1,1,0,0,0,0,time.UTC)
 left:=backtest.Snapshot{Report:backtest.Report{ID:"a",Status:"completed",Config:backtest.Config{From:at,To:at.Add(2*time.Hour),InitialCapital:"1000"}}}
 right:=left; right.Report.ID="b"; right.Report.Config.From=at.Add(time.Hour);right.Report.Config.To=at.Add(3*time.Hour);right.Report.Config.InitialCapital="2000"
 r:=backtest.Compare(left,right)
 if r.DirectlyComparable || r.MatchedPeriod==nil || !r.MatchedPeriod.From.Equal(at.Add(time.Hour)) || !r.MatchedPeriod.To.Equal(at.Add(2*time.Hour)) { t.Fatalf("comparison: %+v",r) }
 found:=false;for _,d:=range r.Differences {if d.Field=="config.initial_capital" {found=true}}
 if !found || len(r.Warnings)==0 {t.Fatalf("missing assumptions: %+v",r)}
}
