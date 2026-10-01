/* Disposable gathering fixture, grodt.state/v1. No libc, WASI, or hidden state.
 * Token spans read fields from host-supplied JSON; no substring matching across
 * provenance or payload boundaries. This bounded parser is only for this fixture.
 */
typedef unsigned int u32;
typedef unsigned long long u64;
static char input[8*1024*1024], output[32768];
static struct {u32 start,end,next;char kind;} token[2048];
static u32 position,size,count,used;
static int bad;
__attribute__((export_name("grodt_state_abi_version"))) u32 version(void){return 1;}
__attribute__((export_name("grodt_alloc"))) u32 allocate(u32 n){return n>sizeof(input)?0xffffffffu:(u32)input;}
static u32 length(const char *p){u32 n=0;while(p[n])n++;return n;}
static u64 result(const char *p){return ((u64)(u32)p<<32)|length(p);}
static void space(void){while(position<size&&(input[position]==' '||input[position]=='\n'||input[position]=='\r'||input[position]=='\t'))position++;}
static u32 parse(u32 depth){
 space();if(depth>32||count>=2048||position>=size){bad=1;return 0;}
 u32 t=count++;token[t].start=position;char c=input[position++];token[t].kind=c;
 if(c=='{'||c=='['){
  space();while(position<size&&input[position]!=(c=='{'?'}':']')&&!bad){
   if(input[position]==','||input[position]==':'){position++;continue;}
   parse(depth+1);space();
  }
  if(position>=size)bad=1;else position++;
 }else if(c=='"'){
  int closed=0;
  while(position<size){char x=input[position++];if(x=='\\'){if(position<size)position++;else bad=1;}else if(x=='"'){closed=1;break;}}
  if(!closed)bad=1;
 }else{while(position<size&&input[position]!=','&&input[position]!='}'&&input[position]!=']'&&input[position]!=' '&&input[position]!='\n'&&input[position]!='\r'&&input[position]!='\t')position++;}
 token[t].end=position;token[t].next=count;return t;
}
static int equals(u32 t,const char *s){
 if(t>=count||token[t].kind!='"')return 0;
 u32 n=length(s);if(token[t].end-token[t].start!=n+2)return 0;
 for(u32 i=0;i<n;i++)if(input[token[t].start+i+1]!=s[i])return 0;return 1;
}
static u32 field(u32 object,const char *name){
 if(object>=count||token[object].kind!='{')return 2048;
 for(u32 key=object+1;key<token[object].next;){u32 value=key+1;if(value>=token[object].next)return 2048;if(equals(key,name))return value;key=token[value].next;}
 return 2048;
}
static void append(const char *s){for(u32 i=0;s[i];i++){if(used+1>=sizeof(output)){bad=1;return;}output[used++]=s[i];}output[used]=0;}
static void raw(u32 t){
 if(t>=count){bad=1;return;}
 for(u32 i=token[t].start;i<token[t].end;i++){if(used+1>=sizeof(output)){bad=1;return;}output[used++]=input[i];}output[used]=0;
}
static int integer(u32 t,int *n){
 if(t>=count)return 0;u32 i=token[t].start;int sign=1,value=0;
 if(input[i]=='-'){sign=-1;i++;}if(i==token[t].end)return 0;
 for(;i<token[t].end;i++){char c=input[i];if(c<'0'||c>'9'||value>100000)return 0;value=value*10+c-'0';}*n=value*sign;return 1;
}
static void number(int n){char digits[12];u32 k=0;if(n<0){append("-");n=-n;}do{digits[k++]='0'+n%10;n/=10;}while(n);while(k){char s[2]={digits[--k],0};append(s);}}
__attribute__((export_name("grodt_process"))) u64 process(u32 pointer,u32 n){
 if(pointer!=(u32)input||n>sizeof(input))return result("{\"status\":\"error\"}");
 position=count=used=0;bad=0;size=n;parse(0);space();if(bad||position!=size)return result("{\"status\":\"error\"}");
 u32 event=field(0,"event"),source=field(event,"source"),payload=field(event,"payload");
 if(!equals(field(source,"kind"),"runtime")||!equals(field(source,"id"),"gathering-environment"))return result("{\"status\":\"ignored\"}");
 u32 type=field(payload,"type"),location=field(payload,"location");
 append("{\"status\":\"mutation\",\"patch\":[");
 if(equals(type,"location_discovered")){
  /* Fixture location names are lowercase identifiers, safe in a JSON Pointer. */
  if(location>=count||token[location].kind!='"')bad=1;
  append("{\"op\":\"add\",\"path\":\"/known_locations/");
  if(!bad)for(u32 i=token[location].start+1;i+1<token[location].end;i++){char c=input[i];if(c<'a'||c>'z'){bad=1;break;}char s[2]={c,0};append(s);}
  append("\",\"value\":{\"direction\":");raw(field(payload,"direction"));append(",\"resources\":");raw(field(payload,"resources"));append("}}");
 }else if(equals(type,"location_changed")){
  append("{\"op\":\"replace\",\"path\":\"/location\",\"value\":");raw(location);append("}");
 }else if(equals(type,"resource_gathered")){
  int wood,quantity;u32 current=field(0,"current");
  if(!equals(field(payload,"resource"),"wood")||!integer(field(field(current,"inventory"),"wood"),&wood)||!integer(field(payload,"quantity"),&quantity))bad=1;
  if(!bad){append("{\"op\":\"replace\",\"path\":\"/inventory/wood\",\"value\":");number(wood+quantity);append("}");}
 }else{return result("{\"status\":\"ignored\"}");}
 append("]}");return bad?result("{\"status\":\"error\"}"):result(output);
}
