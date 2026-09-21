package main

import (
	"context"
	"time"
)

func (a *App) runFullOptimize(ctx context.Context, cfg Config) error {
	attempt:=time.Now()
	a.mu.Lock(); a.state.LastOptimizeAttempt=attempt.Format(time.RFC3339); a.mu.Unlock()
	a.markRefreshAttempt(attempt)
	candidates,err:=a.runCFST(ctx,cfg)
	if err!=nil{return err}
	a.storeCandidates(candidates)
	samples:=a.loadSamples(ctx,cfg)

	a.mu.RLock(); current:=copyMappings(a.state.Mappings); health:=copyHealth(a.state.DomainHealth); a.mu.RUnlock()
	mappings:=make(map[string]string); statuses:=make(map[string]string); groupIP:=make(map[string]string)
	now:=time.Now().Format(time.RFC3339)

	for _,d:=range cfg.Domains {
		if !d.Enabled { statuses[d.Host]="disabled"; continue }
		if !domainRefreshable(d,cfg,samples) { statuses[d.Host]="tracker sample missing"; h:=health[d.Host];h.FailureStreak++;h.LastFailure=now;health[d.Host]=h;continue }
		preferred:=groupIP[groupKey(d)]
		order:=orderedCandidates(candidates,d,preferred,"",cfg)
		resolved:=false; lastDetail:="no candidate"
		for _,c:=range order {
			ok,detail:=verifyConfiguredDomain(ctx,d,c.IP,cfg,samples);lastDetail=detail
			if !ok{continue}
			mappings[d.Host]=c.IP;statuses[d.Host]="optimized · "+c.IP+" · "+detail
			if k:=groupKey(d);k!=""&&groupIP[k]==""{groupIP[k]=c.IP}
			resolved=true;break
		}
		if !resolved {
			if oldIP:=current[d.Host];oldIP!="" {
				ok,detail:=verifyConfiguredDomain(ctx,d,oldIP,cfg,samples)
				if ok { mappings[d.Host]=oldIP;statuses[d.Host]="retained after optimize · "+oldIP+" · "+detail;resolved=true }
			}
		}
		h:=health[d.Host]
		if resolved{h.FailureStreak=0;h.LastSuccess=now}else{h.FailureStreak++;h.LastFailure=now;statuses[d.Host]="optimize unresolved · "+lastDetail}
		health[d.Host]=h
	}
	if err:=a.commitResolution(ctx,cfg,mappings,statuses,health,"",true);err!=nil{return err}
	a.appendLog("full optimize completed: %d mappings",len(mappings))
	return nil
}
