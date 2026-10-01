/* Minimal grodt.state/v1 reference for deterministic synthetic events.
 * This fixture uses an integer current value and a small action payload; it is
 * not a general JSON parser or domain module SDK. No libc/WASI imports.
 */
typedef unsigned int u32;
typedef unsigned long long u64;
#ifndef TEST_MODE
#define TEST_MODE 0
#endif
static char input[8*1024*1024];
static char output[128];
static unsigned calls;
__attribute__((export_name("grodt_state_abi_version"))) u32 version(void) { return TEST_MODE==1?2:1; }
__attribute__((export_name("grodt_alloc"))) u32 allocate(u32 length) { return length>sizeof(input)?0xffffffffu:(u32)input; }
static u32 length(const char *p){u32 n=0;while(p[n])n++;return n;}
static char *find(char *p,u32 n,const char *needle){u32 l=length(needle);for(u32 i=0;i+l<=n;i++){u32 j=0;while(j<l&&p[i+j]==needle[j])j++;if(j==l)return p+i+l;}return 0;}
static u64 result(const char *p){return ((u64)(u32)p<<32)|length(p);}
#if TEST_MODE==7
__attribute__((import_module("env"),import_name("forbidden"))) extern void forbidden(void);
#endif
__attribute__((export_name("grodt_process"))) u64 process(u32 pointer,u32 size){
#if TEST_MODE==2
 __builtin_trap();
#elif TEST_MODE==3
 while(1){__asm__ volatile("");}
#elif TEST_MODE==4
 return ((u64)0xffffff00u<<32)|128u;
#elif TEST_MODE==5
 return ((u64)(u32)output<<32)|(5*1024*1024);
#elif TEST_MODE==6
 return result("not json");
#elif TEST_MODE==7
 forbidden();return result("{\"status\":\"ignored\"}");
#elif TEST_MODE==8
 if(__builtin_wasm_memory_grow(0,1024)==(unsigned long)-1)return result("{\"status\":\"processed\"}");
 __builtin_trap();
#elif TEST_MODE==9
 return result("{\"status\":\"unknown\"}");
#else
 char *p=(char*)pointer;
 char *payload=find(p,size,"\"payload\":");if(!payload)return result("{\"status\":\"error\"}");
 u32 remaining=size-(payload-p);
 if(find(payload,remaining,"\"action\":\"request\""))return result("{\"status\":\"ignored\",\"requests\":[{\"id\":\"reference\",\"kind\":\"http\",\"payload\":{\"method\":\"GET\",\"url\":\"http://example.invalid/\"}}]}");
 if(find(payload,remaining,"\"action\":\"processed\""))return result("{\"status\":\"processed\"}");
 if(find(payload,remaining,"\"action\":\"error\""))return result("{\"status\":\"error\"}");
 if(!find(payload,remaining,"\"action\":\"increment\""))return result("{\"status\":\"ignored\"}");
 char *value=find(p,size,"\"current\":");if(!value)return result("{\"status\":\"error\"}");
 u32 number=0;while(*value>='0'&&*value<='9')number=number*10+(*value++-'0');
 number+=++calls; /* A reused instance would incorrectly add 2 on its next call. */
 const char *prefix="{\"status\":\"mutation\",\"replace\":";u32 n=0;while(prefix[n]){output[n]=prefix[n];n++;}
 char digits[10];u32 count=0;do{digits[count++]='0'+number%10;number/=10;}while(number);
 while(count)output[n++]=digits[--count];output[n++]='}';output[n]=0;return result(output);
#endif
}
