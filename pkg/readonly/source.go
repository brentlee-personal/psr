package readonly

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"strconv"
	"time"
	"ragflow/pkg/authn"
	_ "github.com/lib/pq"
)

var identifier=regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)
var digest=regexp.MustCompile(`^[0-9a-f]{64}$`)
func hash(data []byte) string { value:=sha256.Sum256(data);return hex.EncodeToString(value[:]) }
func protected(path string) ([]byte,error) {
	if !filepath.IsAbs(path){return nil,ErrUnverified}
	f,err:=os.Open(path);if err!=nil{return nil,ErrUnverified};defer f.Close()
	info,err:=f.Stat();if err!=nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022!=0{return nil,ErrUnverified}
	data,err:=io.ReadAll(io.LimitReader(f,(1<<20)+1));if err!=nil || len(data)>1<<20{return nil,ErrUnverified};return data,nil
}
func decode(data []byte,out any) error {
	keys:=json.NewDecoder(bytes.NewReader(data))
	if err:=uniqueJSON(keys);err!=nil{return ErrUnverified}
	if _,err:=keys.Token();err!=io.EOF{return ErrUnverified}
	d:=json.NewDecoder(bytes.NewReader(data));d.DisallowUnknownFields()
	if err:=d.Decode(out);err!=nil{return ErrUnverified};return nil
}
func uniqueJSON(d *json.Decoder) error {
	t,err:=d.Token();if err!=nil{return err}
	switch t {
	case json.Delim('{'):
		seen:=map[string]bool{}
		for d.More(){t,err:=d.Token();if err!=nil{return err};name,ok:=t.(string);name=strings.ToLower(name);if !ok || seen[name]{return ErrUnverified};seen[name]=true;if err:=uniqueJSON(d);err!=nil{return err}}
		if t,err=d.Token();err!=nil || t!=json.Delim('}'){return ErrUnverified}
	case json.Delim('['):
		for d.More(){if err:=uniqueJSON(d);err!=nil{return err}};if t,err=d.Token();err!=nil || t!=json.Delim(']'){return ErrUnverified}
	}
	return nil
}

func connectionFiles(path string) (string,string,error) {
	if !filepath.IsAbs(path){return "","",ErrUnverified}
	values:=map[string]string{}
	for _,key:=range []string{"host","port","database","user","password","sslmode"}{raw,err:=protected(filepath.Join(path,key));if err!=nil || len(raw)==0{return "","",ErrUnverified};values[key]=string(raw)}
	port,err:=strconv.Atoi(values["port"]);if err!=nil || port<1 || port>65535{return "","",ErrUnverified}
	if strings.ContainsAny(values["host"],"/\x00\r\n") || !identifier.MatchString(values["database"]) || !identifier.MatchString(values["user"]){return "","",ErrUnverified}
	switch values["sslmode"]{case "disable","require","verify-ca","verify-full":default:return "","",ErrUnverified}
	endpoint:=url.URL{Scheme:"postgres",User:url.UserPassword(values["user"],values["password"]),Host:net.JoinHostPort(values["host"],values["port"]),Path:"/"+values["database"]}
	endpoint.RawQuery=url.Values{"sslmode":[]string{values["sslmode"]},"connect_timeout":[]string{"5"},"application_name":[]string{"tob-readonly"}}.Encode()
	data,_:=json.Marshal(values);return endpoint.String(),hash(data),nil
}

// Open uses a separate protected business connection. It never borrows dao.DB,
// creates a database/role/table, runs migrations or accepts a model-supplied DSN.
func Open(ctx context.Context,policyPath,connectionPath string,verifier *authn.Verifier) (*Source,error) {
	if verifier==nil || ctx==nil{return nil,ErrUnverified}
	raw,err:=protected(policyPath);if err!=nil{return nil,initializationFailure("policy_read")};var initial Policy;if decode(raw,&initial)!=nil{return nil,initializationFailure("policy_decode")}
 dialect:=initial.Dialect;if dialect==""{dialect="postgresql"};if dialect!="postgresql" && dialect!="starrocks"{return nil,initializationFailure("unsupported_dialect")}
 connection:=connectionFiles;driverName:="postgres";if dialect=="starrocks"{connection=starrocksConnection;driverName="mysql"}
 dsn,connectionHash,err:=connection(connectionPath);if err!=nil{return nil,initializationFailure("connection_reference")}
 db,err:=sql.Open(driverName,dsn);if err!=nil{return nil,initializationFailure("driver_registration")}
	db.SetMaxOpenConns(2);db.SetMaxIdleConns(0)
	s:=&Source{dialect:dialect,db:db,verifier:verifier,policyPath:policyPath,connectionPath:connectionPath,connectionHash:connectionHash,slots:make(chan struct{},2)}
	if _,err:=s.readPolicy();err!=nil{db.Close();return nil,initializationFailure("active_source_policy")}
	if err:=db.PingContext(ctx);err!=nil{db.Close();return nil,initializationFailure("connection_ping")}
	return s,nil
}
func (s *Source) readPolicy() (Authority,error) {
	connection:=connectionFiles;if s.dialect=="starrocks"{connection=starrocksConnection}
 _,connectionHash,err:=connection(s.connectionPath);if err!=nil || connectionHash!=s.connectionHash{return Authority{},ErrUnverified}
	raw,err:=protected(s.policyPath);if err!=nil{return Authority{},err}
	var p Policy
	if decode(raw,&p)!=nil{return Authority{},ErrUnverified}
 dialect:=p.Dialect;if dialect==""{dialect="postgresql"};expected:=s.dialect;if expected==""{expected="postgresql"};if dialect!=expected{return Authority{},ErrUnverified}
 if dialect=="starrocks"{if validateStarRocks(p)!=nil{return Authority{},ErrUnverified}} else if p.StarRocks!=nil || !identifier.MatchString(p.Database) || !identifier.MatchString(p.Role) || !identifier.MatchString(p.Schema) || !identifier.MatchString(p.Table) || p.ExpiresAt.IsZero() || len(p.Columns)==0 || len(p.Columns)>64 || len(p.Functions)>32 {return Authority{},ErrUnverified}
	for _,v:=range []string{p.SourceID,p.UserID,p.TenantID,p.RelationID,p.ConnectionRef,p.ReadonlyIdentityRef,p.TargetID,p.SnapshotRef,p.EnforcementRef,p.LimitsRef}{if v=="" || len(v)>192{return Authority{},ErrUnverified}}
	for _,v:=range []string{p.KnowledgeSHA256,p.PlannerSHA256,p.DataBindingSHA256}{if !digest.MatchString(v){return Authority{},ErrUnverified}}
	seen:=map[string]bool{}
	for _,c:=range p.Columns{if !identifier.MatchString(c.Name) || c.Type=="" || seen[c.Name]{return Authority{},ErrUnverified};seen[c.Name]=true}
	seen=map[string]bool{}
	for _,f:=range p.Functions{parts:=strings.Split(f,".");if len(parts)!=2 || parts[0]!="pg_catalog" || !identifier.MatchString(parts[1]) || seen[f]{return Authority{},ErrUnverified};seen[f]=true}
	if p.State!="ACTIVE" && p.State!="SUSPENDED"{return Authority{},ErrUnverified}
	if p.State!="ACTIVE" || !time.Now().Before(p.ExpiresAt){return Authority{},ErrDenied}
	return Authority{Policy:p,SHA256:hash(raw)},nil
}

// Authority reads current native identity, current protected policy and actual
// database/role/catalog privileges. It returns metadata, never DSN or data rows.
func (s *Source) Authority(ctx context.Context,identity authn.VerifiedIdentity) (Authority,error) {
	if err:=s.CheckIdentity(ctx,identity);err!=nil{return Authority{},err}
	a,err:=s.readPolicy();if err!=nil{return Authority{},err};p:=a.Policy;snapshot:=identity.Snapshot()
	if snapshot.UserID!=p.UserID || snapshot.TenantID!=p.TenantID{return Authority{},ErrDenied}
 if s.dialect=="starrocks"{return s.starrocksAuthority(ctx,a)}
	var database,user,session string;var super,createRole,createDB,replicate,bypass bool
	err=s.db.QueryRowContext(ctx,"SELECT current_database(), current_user, session_user, rolsuper, rolcreaterole, rolcreatedb, rolreplication, rolbypassrls FROM pg_catalog.pg_roles WHERE rolname=current_user").Scan(&database,&user,&session,&super,&createRole,&createDB,&replicate,&bypass)
	if err!=nil || database!=p.Database || user!=p.Role || session!=p.Role || super || createRole || createDB || replicate || bypass{return Authority{},ErrUnverified}
	rows,err:=s.db.QueryContext(ctx,"SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),pg_catalog.has_column_privilege(current_user,c.oid,a.attnum,'SELECT') FROM pg_catalog.pg_attribute a JOIN pg_catalog.pg_class c ON a.attrelid=c.oid JOIN pg_catalog.pg_namespace n ON c.relnamespace=n.oid WHERE n.nspname=$1 AND c.relname=$2 AND c.relkind='r' AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum",p.Schema,p.Table)
	if err!=nil{return Authority{},ErrUnverified}
	columns:=map[string]string{};for rows.Next(){var name,kind string;var allowed bool;if rows.Scan(&name,&kind,&allowed)!=nil || !allowed || len(columns)>=64{rows.Close();return Authority{},ErrUnverified};columns[name]=kind}
	err=rows.Err();closeErr:=rows.Close();if err!=nil || closeErr!=nil || len(columns)!=len(p.Columns){return Authority{},ErrUnverified}
	for _,c:=range p.Columns{if columns[c.Name]!=c.Type{return Authority{},ErrUnverified}}
	for _,name:=range p.Functions{
		function:=strings.Split(name,".")[1]
		rows,err:=s.db.QueryContext(ctx,"SELECT p.provolatile,p.prosecdef FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON p.pronamespace=n.oid WHERE n.nspname='pg_catalog' AND p.proname=$1",function)
		if err!=nil{return Authority{},ErrUnverified};count:=0
		for rows.Next(){var volatility string;var definer bool;if rows.Scan(&volatility,&definer)!=nil || (volatility!="i" && volatility!="s") || definer || count>=64{rows.Close();return Authority{},ErrUnverified};count++}
		err=rows.Err();closeErr=rows.Close();if err!=nil || closeErr!=nil || count==0{return Authority{},ErrUnverified}
	}
	latest,err:=s.readPolicy();if err!=nil || latest.SHA256!=a.SHA256 || ctx.Err()!=nil{return Authority{},ErrUnverified}
	return a,nil
}
