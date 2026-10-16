//go:build ignore

// This build-time adapter makes the selected tokenizer's read-only literal
// tables invocation-local. It is not part of the printed Process.
package main

import (
 "bytes"
 "go/ast"
 "go/format"
 "go/parser"
 "go/token"
 "os"
 "path/filepath"
)

func main(){
 directory:=os.Args[1];fset:=token.NewFileSet();files:=map[string]*ast.File{};names:=map[string]bool{}
 entries,err:=os.ReadDir(directory);if err!=nil{panic(err)}
 for _,entry:=range entries{if filepath.Ext(entry.Name())!=".go"{continue};path:=filepath.Join(directory,entry.Name());f,e:=parser.ParseFile(fset,path,nil,parser.ParseComments);if e!=nil{panic(e)};files[path]=f
  for _,d:=range f.Decls{if g,ok:=d.(*ast.GenDecl);ok&&g.Tok==token.VAR{for _,s:=range g.Specs{v:=s.(*ast.ValueSpec);if len(v.Names)!=1||len(v.Values)!=1{panic("only explicitly initialized single literal tables supported")};names[v.Names[0].Name]=true}}}
 }
 for path,file:=range files{
  declarations:=[]ast.Decl{}
  for _,d:=range file.Decls{
   if g,ok:=d.(*ast.GenDecl);ok&&g.Tok==token.VAR{
    for _,s:=range g.Specs{
     v:=s.(*ast.ValueSpec);value:=v.Values[0];typ:=v.Type
     if typ==nil{switch x:=value.(type){case *ast.CompositeLit:typ=x.Type;case *ast.CallExpr:if id,ok:=x.Fun.(*ast.ArrayType);ok{typ=id}else{typ=ast.NewIdent("error")};default:panic("unsupported initializer")}}
     if array,ok:=typ.(*ast.ArrayType);ok{if _,ellipsis:=array.Len.(*ast.Ellipsis);ellipsis{array.Len=nil}}
     declarations=append(declarations,&ast.FuncDecl{Name:v.Names[0],Type:&ast.FuncType{Params:&ast.FieldList{},Results:&ast.FieldList{List:[]*ast.Field{{Type:typ}}}},Body:&ast.BlockStmt{List:[]ast.Stmt{&ast.ReturnStmt{Results:[]ast.Expr{value}}}}})
    }
   }else{declarations=append(declarations,d)}
  }
  file.Decls=declarations
  // Replace identifier uses through expression slots; declaration names and
  // selectors remain identifiers. Selected source has no local shadowing of
  // these audited table names.
  var expr func(ast.Expr)ast.Expr
  expr=func(e ast.Expr)ast.Expr{if id,ok:=e.(*ast.Ident);ok&&names[id.Name]{return &ast.CallExpr{Fun:id}};return e}
  ast.Inspect(file,func(n ast.Node)bool{
   switch x:=n.(type){
   case *ast.IndexExpr:x.X=expr(x.X)
   case *ast.SliceExpr:x.X=expr(x.X)
   case *ast.CallExpr:for i:=range x.Args{x.Args[i]=expr(x.Args[i])}
   case *ast.RangeStmt:x.X=expr(x.X)
   case *ast.ReturnStmt:for i:=range x.Results{x.Results[i]=expr(x.Results[i])}
   case *ast.BinaryExpr:x.X=expr(x.X);x.Y=expr(x.Y)
   case *ast.AssignStmt:for i:=range x.Rhs{x.Rhs[i]=expr(x.Rhs[i])}
   }
   return true
  })
  var out bytes.Buffer;if err:=format.Node(&out,fset,file);err!=nil{panic(err)};if err:=os.WriteFile(path,out.Bytes(),0600);err!=nil{panic(err)}
 }
}
