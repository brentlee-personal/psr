package readonly

import (
 "encoding/json"
 "testing"
 "ragflow/internal/readonly/sqlresult"
)

func TestReadonlyNumericOutputPreservesDecimalAndBinary(t *testing.T) {
 read:=sqlresult.ReadResult{
  Columns:[]sqlresult.Column{{Name:"amount",DatabaseType:"NUMERIC"},{Name:"decimal",DatabaseType:"DECIMAL"},{Name:"binary",DatabaseType:"BYTEA"},{Name:"nullable",DatabaseType:"NUMERIC"}},
  Rows:[][]any{{[]byte("12345678901234567890.12345678901234567890"),[]byte("-0.01"),[]byte{0,255},nil}},
 }
 rows,err:=publicRows(read,1<<20);if err!=nil{t.Fatal(err)}
 encoded,err:=json.Marshal(rows);if err!=nil{t.Fatal(err)}
 const want=`[["12345678901234567890.12345678901234567890","-0.01","AP8=",null]]`
 if string(encoded)!=want{t.Fatalf("decimal or binary wire value changed: %s",encoded)}
 if _,ok:=read.Rows[0][0].([]byte);!ok{t.Fatal("borrowed reader result was mutated")}
 if rows,err:=publicRows(read,1);err==nil || rows!=nil{t.Fatal("converted output escaped byte limit")}
}
