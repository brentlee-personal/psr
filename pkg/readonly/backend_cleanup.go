package readonly

import (
 "context"
 "database/sql"
 "errors"
 "time"
)

// Private identity only; never returned, logged or supplied by a caller/model.
// Server endpoint/start plus database/role OIDs prevent an unrelated instance
// or an obscured activity row from being mistaken for the retired backend.
type backendIdentity struct {
 pid int
 started, serverStarted time.Time
 databaseOID, roleOID int64
 address string
 port int
}

func readOwnBackend(ctx context.Context,conn *sql.Conn,policy Policy)(backendIdentity,error){
 var own backendIdentity
 var database,role,currentRole,sessionRole string
 var address sql.NullString
 var port sql.NullInt64
 err:=conn.QueryRowContext(ctx,`SELECT a.pid,a.backend_start,a.datid::bigint,a.usesysid::bigint,a.datname,a.usename,current_user,session_user,pg_catalog.inet_server_addr()::text,pg_catalog.inet_server_port(),pg_catalog.pg_postmaster_start_time() FROM pg_catalog.pg_stat_activity a WHERE a.pid=pg_catalog.pg_backend_pid()`).Scan(&own.pid,&own.started,&own.databaseOID,&own.roleOID,&database,&role,&currentRole,&sessionRole,&address,&port,&own.serverStarted)
 if err!=nil || ctx.Err()!=nil || own.pid<=0 || own.started.IsZero() || own.serverStarted.IsZero() || own.databaseOID<=0 || own.roleOID<=0 || database!=policy.Database || role!=policy.Role || currentRole!=policy.Role || sessionRole!=policy.Role || !address.Valid || address.String=="" || !port.Valid || port.Int64<=0 || port.Int64>65535{return backendIdentity{},ErrUnverified}
 own.address=address.String;own.port=int(port.Int64)
 return own,nil
}

// Called only after query-time cancellation and this Source's synchronous
// retirement (nil Close, or its exact ErrBadConn rollback/ErrConnDone chain).
// MaxIdleConns remains zero; error codes alone never prove backend exit.
// The finite Source-owned context authorizes metadata cleanup only, never SQL
// beyond the business context. The 500ms probe inherits its earlier deadline.
func (s *Source) confirmRetiredBackend(lifecycle context.Context,retired backendIdentity,policy Policy)bool{
 ctx,cancel:=context.WithTimeout(lifecycle,500*time.Millisecond)
 defer cancel()
 observer,err:=s.db.Conn(ctx);if err!=nil{return false}
 closed:=false
 defer func(){if !closed{observer.Close()}}()
 own,err:=readOwnBackend(ctx,observer,policy)
 if err!=nil || own.address!=retired.address || own.port!=retired.port || !own.serverStarted.Equal(retired.serverStarted) || own.databaseOID!=retired.databaseOID || own.roleOID!=retired.roleOID{return false}
 for {
  var started sql.NullTime
  var databaseOID,roleOID sql.NullInt64
  err=observer.QueryRowContext(ctx,`SELECT backend_start,datid::bigint,usesysid::bigint FROM pg_catalog.pg_stat_activity WHERE pid=$1`,retired.pid).Scan(&started,&databaseOID,&roleOID)
  gone:=errors.Is(err,sql.ErrNoRows)
  if err==nil{
   // A hidden/NULL identity cannot prove exit. A different visible start
   // proves PID reuse; the precise old backend no longer exists.
   if !started.Valid || !databaseOID.Valid || !roleOID.Valid{return false}
   gone=!started.Time.Equal(retired.started)
   if !gone && (databaseOID.Int64!=retired.databaseOID || roleOID.Int64!=retired.roleOID){return false}
  }else if !gone{return false}
  if ctx.Err()!=nil{return false}
  if gone{
   closeErr:=observer.Close();closed=true
   return closeErr==nil && ctx.Err()==nil
  }
  timer:=time.NewTimer(20*time.Millisecond)
  select{case <-ctx.Done():timer.Stop();return false;case <-timer.C:}
 }
}
