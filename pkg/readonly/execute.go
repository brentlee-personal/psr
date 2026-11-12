package readonly

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"
	"ragflow/internal/readonly/sqlguard"
	"ragflow/internal/readonly/sqlresult"
)

// Approve uses the existing PostgreSQL AST guard and seals exact SQL, current
// native identity, source policy, scope, limits and expiry to this Source.
func (s *Source) Approve(ctx context.Context,request Request) (Approval,error) {
	if s==nil || s.uncertain.Load() || request.MaxRows<1 || request.MaxRows>100 || request.MaxBytes<1 || request.MaxBytes>1<<20 || request.Timeout<=0 || request.Timeout>30*time.Second || !digest.MatchString(request.Fingerprint){return Approval{},ErrUnverified}
	a,err:=s.Authority(ctx,request.Identity);if err!=nil{return Approval{},err}
	if request.ScopeSHA256!=a.SHA256{return Approval{},ErrDenied}
	names:=make([]string,len(a.Policy.Columns));for i,c:=range a.Policy.Columns{names[i]=c.Name}
	functions:=map[string]bool{};for _,f:=range a.Policy.Functions{functions[f]=true}
	analysis,err:=sqlguard.Analyze(ctx,request.SQL,sqlguard.Policy{Dialect:"postgresql",Relations:map[string]sqlguard.Relation{a.Policy.Schema+"."+a.Policy.Table:{Columns:names,Complete:true}},Functions:functions})
	if err!=nil || len(analysis.Relations)!=1 || analysis.Relations[0]!=a.Policy.Schema+"."+a.Policy.Table{return Approval{},ErrDenied}
	if err:=s.CheckIdentity(ctx,request.Identity);err!=nil{return Approval{},err}
	latest,err:=s.readPolicy();if err!=nil || latest.SHA256!=a.SHA256 || ctx.Err()!=nil{return Approval{},ErrUnverified}
	expires:=a.Policy.ExpiresAt
	if t:=request.Identity.Snapshot().ExpiresAt;t.Before(expires){expires=t}
	if deadline,ok:=ctx.Deadline();ok && deadline.Before(expires){expires=deadline}
	if !time.Now().Before(expires){return Approval{},ErrDenied}
	var random [16]byte;if _,err:=rand.Read(random[:]);err!=nil{return Approval{},ErrUnverified}
	return Approval{proof:&approvalProof{issuer:s,request:request,id:"approval:"+hex.EncodeToString(random[:]),expires:expires}},nil
}

// Execute has no Agent/tool callback. It performs one user statement inside a
// native read-only transaction, then waits synchronously for rollback and close.
func (s *Source) Execute(ctx context.Context,approval Approval) (result Result,err error) {
	p:=approval.proof
	if s==nil || p==nil || p.issuer!=s || !p.used.CompareAndSwap(false,true){return Result{},ErrDenied}
	result=Result{Fingerprint:p.request.Fingerprint,Terminal:"failed",CleanupConfirmed:true}
	if s.uncertain.Load() || !time.Now().Before(p.expires){return result,ErrDenied}
	ctx,cancel:=context.WithTimeout(ctx,p.request.Timeout);defer cancel()
	ctx,deadlineCancel:=context.WithDeadline(ctx,p.expires);defer deadlineCancel()
	a,err:=s.Authority(ctx,p.request.Identity);if err!=nil{return result,err}
	if a.SHA256!=p.request.ScopeSHA256{return result,ErrDenied}
	select{case s.slots<-struct{}{}:default:return result,ErrDenied}
	retain:=false;defer func(){if !retain{<-s.slots}}()
	conn,err:=s.db.Conn(ctx);if err!=nil{return result,ErrExecution}
	result.CleanupConfirmed=false
	tx,err:=conn.BeginTx(ctx,&sql.TxOptions{ReadOnly:true,Isolation:sql.LevelReadCommitted})
	if err!=nil{conn.Close();s.uncertain.Store(true);retain=true;return result,ErrCleanup}
	var readOnly string
	if err:=tx.QueryRowContext(ctx,"SHOW transaction_read_only").Scan(&readOnly);err!=nil || readOnly!="on"{
		rollbackErr,closeErr:=tx.Rollback(),conn.Close()
		result.CleanupConfirmed=rollbackErr==nil && closeErr==nil
		if !result.CleanupConfirmed{s.uncertain.Store(true);retain=true;return result,ErrCleanup};return result,ErrUnverified
	}
	read,queryErr:=sqlresult.Query(ctx,tx,p.request.SQL,nil,sqlresult.Limits{MaxRows:p.request.MaxRows,MaxBytes:p.request.MaxBytes})
	rollbackErr:=tx.Rollback()
	closeErr:=conn.Close()
	result.CleanupConfirmed=rollbackErr==nil && closeErr==nil
	if !result.CleanupConfirmed{s.uncertain.Store(true);retain=true;return result,ErrCleanup}
	if ctx.Err()!=nil{result.Terminal="cancelled";return result,ctx.Err()}
	if queryErr!=nil || read.Completeness!=sqlresult.Complete || read.RowsClose!=sqlresult.CloseConfirmed{return result,ErrExecution}
	latest,err:=s.Authority(ctx,p.request.Identity);if err!=nil || latest.SHA256!=p.request.ScopeSHA256{return result,ErrDenied}
	result.Terminal="completed";result.Complete=true
	result.Columns=make([]string,len(read.Columns));for i,c:=range read.Columns{result.Columns[i]=c.Name}
	result.Rows=read.Rows
	return result,nil
}
