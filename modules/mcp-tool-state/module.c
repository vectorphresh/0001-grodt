/* Generic latest admitted MCP result reducer, grodt.state/v1.
 * Import-free; all authority is in the host-supplied current value and event.
 * JSON spans preserve result values (including numbers) without re-encoding.
 * The host validates JSON syntax and the partition/result schemas. This bounded
 * parser also checks grammar and the reducer's required structural fields.
 */
typedef unsigned int u32;
typedef unsigned long long u64;
#define INPUT_SIZE (8*1024*1024)
#define OUTPUT_SIZE (4*1024*1024)
#define TOKENS 262144
#define NONE TOKENS
static char input[INPUT_SIZE], output[OUTPUT_SIZE];
static struct {u32 start,end,next;char kind;} token[TOKENS];
static u32 position,size,count,used;
static int bad;
__attribute__((export_name("grodt_state_abi_version"))) u32 version(void){return 1;}
__attribute__((export_name("grodt_alloc"))) u32 allocate(u32 n){return n>INPUT_SIZE?0xffffffffu:(u32)input;}
static u32 length(const char *s){u32 n=0;while(s[n])n++;return n;}
static u64 reply(const char *s){return ((u64)(u32)s<<32)|length(s);}
static void space(void){while(position<size&&(input[position]==' '||input[position]=='\n'||input[position]=='\r'||input[position]=='\t'))position++;}
static int hex(char c){if(c>='0'&&c<='9')return c-'0';if(c>='a'&&c<='f')return c-'a'+10;if(c>='A'&&c<='F')return c-'A'+10;return -1;}
static void string(void){
 if(position>=size||input[position++]!='"'){bad=1;return;}
 while(position<size){unsigned char c=input[position++];if(c=='"')return;if(c<32){bad=1;return;}
  if(c=='\\'){if(position>=size){bad=1;return;}c=input[position++];
   if(c=='u'){for(u32 i=0;i<4;i++)if(position>=size||hex(input[position++])<0){bad=1;return;}}
   else if(c!='"'&&c!='\\'&&c!='/'&&c!='b'&&c!='f'&&c!='n'&&c!='r'&&c!='t'){bad=1;return;}
  }
 }bad=1;
}
static void literal(const char *s){for(u32 i=0;s[i];i++)if(position>=size||input[position++]!=s[i]){bad=1;return;}}
static int digit(void){return position<size&&input[position]>='0'&&input[position]<='9';}
static void number(void){
 if(position<size&&input[position]=='-')position++;
 if(!digit()){bad=1;return;}
 if(input[position]=='0')position++;else while(digit())position++;
 if(position<size&&input[position]=='.'){position++;if(!digit())bad=1;while(digit())position++;}
 if(position<size&&(input[position]=='e'||input[position]=='E')){position++;if(position<size&&(input[position]=='+'||input[position]=='-'))position++;if(!digit())bad=1;while(digit())position++;}
}
static u32 parse(u32 depth){
 space();if(bad||depth>256||count>=TOKENS||position>=size){bad=1;return NONE;}
 u32 t=count++;token[t].start=position;char c=input[position];token[t].kind=c;
 if(c=='{'||c=='['){
  position++;space();char close=c=='{'?'}':']';
  if(position<size&&input[position]==close)position++;
  else for(;;){
   if(c=='{'){space();if(position>=size||input[position]!='"'){bad=1;break;}parse(depth+1);space();if(position>=size||input[position++]!=':'){bad=1;break;}}
   parse(depth+1);space();if(bad||position>=size){bad=1;break;}
   char next=input[position++];if(next==close)break;if(next!=','){bad=1;break;}
  }
 }else if(c=='"')string();else if(c=='t')literal("true");else if(c=='f')literal("false");else if(c=='n')literal("null");else number();
 token[t].end=position;token[t].next=count;return t;
}
/* Decode only object-key comparisons. Result strings remain raw spans. */
static u32 unit(u32 *p,u32 end){
 if(*p>=end){bad=1;return 0;}
 unsigned char c=input[(*p)++];
 if(c=='\\'){
  if(*p>=end){bad=1;return 0;}c=input[(*p)++];
  if(c=='u'){
   u32 v=0;for(u32 i=0;i<4;i++){if(*p>=end){bad=1;return 0;}int h=hex(input[(*p)++]);if(h<0){bad=1;return 0;}v=(v<<4)|(u32)h;}
   if(v>=0xd800&&v<=0xdbff){
    if(*p+6>end||input[*p]!='\\'||input[*p+1]!='u'){bad=1;return 0;}*p+=2;
    u32 lo=0;for(u32 i=0;i<4;i++){int h=hex(input[(*p)++]);if(h<0){bad=1;return 0;}lo=(lo<<4)|(u32)h;}
    if(lo<0xdc00||lo>0xdfff){bad=1;return 0;}return 0x10000+((v-0xd800)<<10)+(lo-0xdc00);
   }
   if(v>=0xdc00&&v<=0xdfff)bad=1;return v;
  }
  if(c=='b')return 8;if(c=='f')return 12;if(c=='n')return 10;if(c=='r')return 13;if(c=='t')return 9;return c;
 }
 if(c<128)return c;
 u32 n,v;if((c&224)==192){n=1;v=c&31;}else if((c&240)==224){n=2;v=c&15;}else if((c&248)==240){n=3;v=c&7;}else{bad=1;return 0;}
 while(n--){if(*p>=end||(input[*p]&192)!=128){bad=1;return 0;}v=(v<<6)|(input[(*p)++]&63);}return v;
}
static int kind(u32 t,char c){return t<count&&token[t].kind==c;}
static int same(u32 a,u32 b){
 if(!kind(a,'"')||!kind(b,'"'))return 0;
 u32 x=token[a].start+1,y=token[b].start+1,xe=token[a].end-1,ye=token[b].end-1;
 while(x<xe&&y<ye&&!bad){if(unit(&x,xe)!=unit(&y,ye))return 0;}return !bad&&x==xe&&y==ye;
}
static int equals(u32 t,const char *s){
 if(!kind(t,'"'))return 0;u32 p=token[t].start+1,end=token[t].end-1,i=0;
 while(p<end&&s[i]&&!bad)if(unit(&p,end)!=(unsigned char)s[i++])return 0;
 return !bad&&p==end&&!s[i];
}
static u32 field(u32 t,const char *name){
 if(!kind(t,'{'))return NONE;u32 found=NONE;
 for(u32 k=t+1;k<token[t].next;k=token[k+1].next)if(equals(k,name)){if(found!=NONE)bad=1;found=k+1;}
 return found;
}
static u32 lookup(u32 t,u32 key){
 if(!kind(t,'{'))return NONE;u32 found=NONE;
 for(u32 k=t+1;k<token[t].next;k=token[k+1].next)if(same(k,key)){if(found!=NONE)bad=1;found=k+1;}
 return found;
}
static int sequence(u32 t,u64 *value){
 if(t>=count)return 0;u64 n=0;
 for(u32 p=token[t].start;p<token[t].end;p++){char c=input[p];if(c<'0'||c>'9'||n>(~(u64)0-(u64)(c-'0'))/10)return 0;n=n*10+(c-'0');}
 *value=n;return 1;
}
static int envelope(u32 r){
 if(!kind(r,'{')||!kind(field(r,"content"),'['))return 0;
 u32 error=field(r,"isError");return error==NONE||kind(error,'t')||kind(error,'f');
}
static int current_valid(u32 current,u32 sources){
 if(!kind(current,'{')||!kind(sources,'{'))return 0;
 for(u32 k=current+1;k<token[current].next;k=token[k+1].next)if(!equals(k,"sources"))return 0;
 for(u32 s=sources+1;s<token[sources].next;s=token[s+1].next){
  u32 tools=s+1;if(!kind(tools,'{'))return 0;
  for(u32 t=tools+1;t<token[tools].next;t=token[t+1].next){
   u32 leaf=t+1;u64 seq;
   if(!kind(leaf,'{')||!sequence(field(leaf,"sequence"),&seq)||!kind(field(leaf,"timestamp"),'"')||!envelope(field(leaf,"result")))return 0;
   u32 correlation=field(leaf,"correlation");if(correlation!=NONE&&!kind(correlation,'{'))return 0;
  }
 }return !bad;
}
static void append(const char *s){for(u32 i=0;s[i]&&!bad;i++){if(used+1>=OUTPUT_SIZE){bad=1;return;}output[used++]=s[i];}output[used]=0;}
static void raw(u32 t){if(t>=count){bad=1;return;}for(u32 p=token[t].start;p<token[t].end&&!bad;p++){if(used+1>=OUTPUT_SIZE){bad=1;return;}output[used++]=input[p];}output[used]=0;}
static void leaf(u32 seq,u32 timestamp,u32 correlation,u32 result){
 append("{\"sequence\":");raw(seq);append(",\"timestamp\":");raw(timestamp);
 if(correlation!=NONE){append(",\"correlation\":");raw(correlation);}
 append(",\"result\":");raw(result);append("}");
}
static void tools(u32 existing,u32 tool,u32 seq,u32 timestamp,u32 correlation,u32 result){
 append("{");int first=1,found=0;
 if(existing!=NONE)for(u32 k=existing+1;k<token[existing].next;k=token[k+1].next){
  if(!first)append(",");first=0;raw(k);append(":");
  if(same(k,tool)){found=1;leaf(seq,timestamp,correlation,result);}else raw(k+1);
 }
 if(!found){if(!first)append(",");raw(tool);append(":");leaf(seq,timestamp,correlation,result);}append("}");
}
__attribute__((export_name("grodt_process"))) u64 process(u32 pointer,u32 n){
 if(pointer!=(u32)input||n>INPUT_SIZE)return reply("{\"status\":\"error\"}");
 position=count=used=0;size=n;bad=0;parse(0);space();
 if(bad||position!=size)return reply("{\"status\":\"error\"}");
 u32 event=field(0,"event"),source=field(event,"source");
 if(!equals(field(source,"kind"),"mcp"))return reply("{\"status\":\"ignored\"}");
 u32 id=field(source,"id"),payload=field(event,"payload"),tool=field(payload,"tool"),result=field(payload,"result");
 u32 seq=field(event,"sequence"),timestamp=field(event,"timestamp"),correlation=field(event,"correlation");
 u32 current=field(0,"current"),sources=field(current,"sources");u64 incoming,previous;
 if(!kind(id,'"')||token[id].end-token[id].start<=2||!kind(tool,'"')||token[tool].end-token[tool].start<=2||!sequence(seq,&incoming)||!kind(timestamp,'"')||(correlation!=NONE&&!kind(correlation,'{'))||!envelope(result)||!current_valid(current,sources)||bad)return reply("{\"status\":\"error\"}");
 u32 existing=lookup(sources,id),old=lookup(existing,tool);
 if(old!=NONE&&(!sequence(field(old,"sequence"),&previous)||bad))return reply("{\"status\":\"error\"}");
 if(old!=NONE&&incoming<=previous)return reply("{\"status\":\"ignored\"}");
 append("{\"status\":\"mutation\",\"replace\":{\"sources\":{");int first=1,found=0;
 for(u32 k=sources+1;k<token[sources].next;k=token[k+1].next){
  if(!first)append(",");first=0;raw(k);append(":");
  if(same(k,id)){found=1;tools(k+1,tool,seq,timestamp,correlation,result);}else raw(k+1);
 }
 if(!found){if(!first)append(",");raw(id);append(":");tools(NONE,tool,seq,timestamp,correlation,result);}
 append("}}}");return bad?reply("{\"status\":\"error\"}"):reply(output);
}
