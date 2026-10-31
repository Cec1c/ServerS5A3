// Festival popup. Native controls, native font and original popup frame.
// Include after festival_host.h in a statically integrated client component.
#include "festival_layout.h"
#include "festival_labels.h"
namespace festivalui {
static bool creating,shown,placed,ready,received,failed;
static void *win,*mgr,*panel;static int id;static unsigned tab,page,sequence,pending,last,since;
static unsigned char data[1568],incoming[1568];static volatile long lock;
static const wchar_t path[]=L"UI/PrivateFestival005/Festival.xui";
static bool owns(bool active){if(!win||!readable(mgr,0xc640)||*(void**)0x56a3798!=mgr)return false;auto h=(char*)mgr+(id*5+(active?0x41:0x1879))*4;auto b=value(h,4),e=value(h,8);if(e<b||e-b>64||(e-b)%4||!readable((void*)b,(unsigned)(e-b)))return false;for(;b<e;b+=4)if(*(void**)b==win)return true;return false;}
static void* control(const wchar_t*name){if(!panel)return nullptr;Ref ref{};((Ref*(__thiscall*)(void*,Ref*,const wchar_t*))0x37eb120)(panel,&ref,name);void*p=ref.value;((void(__thiscall*)(Ref*))0xf11f90)(&ref);return p;}
static void label(const wchar_t*name,const wchar_t*str,unsigned color=0xffe3dfd2){if(!shown||!owns(true))return;void*p=control(name);if(!readable(p,0x410)||value(p)!=0x4d4fa4c){failed=true;return;}((void(__thiscall*)(void*,const wchar_t*))0x3835940)(p,str);((void(__thiscall*)(void*,unsigned))0x3835710)(p,color);}
static void indexedLabel(const wchar_t*prefix,unsigned n,const wchar_t*str,unsigned color=0xffe3dfd2){wchar_t key[40];AP_wsprintfW(key,L"%s%u",prefix,n);label(key,str,color);}
static const wchar_t* rewardName(unsigned item){for(auto&v:kFestivalLabels)if(v.id==item)return v.name;return L"奖励礼盒";}
static void deco(const wchar_t*key,unsigned frame,unsigned w,unsigned h){void*p=control(key);if(!readable(p,0x504)){failed=true;return;}((void(__thiscall*)(void*,int,int,int,int,int))0x3855fc0)(p,frame,frame,frame,frame,-1);*(unsigned*)((char*)p+0x484)=0x3f000000;*(unsigned*)((char*)p+0x488)=0x3f000000;((void(__thiscall*)(void*,int,int))0x38271c0)(p,w,h);}
static void buttonText(const wchar_t*key,const wchar_t*str,const wchar_t*button){label(key,str,0xff6bd1f4);void*p=control(key),*b=control(button);if(!readable(p,0x410)||!readable(b,0x84))return;int w=((int(__thiscall*)(void*))0x3835890)(p);int bw=(int)value(b,0x6c);if(w>=0&&w<=bw)((void(__thiscall*)(void*,int,int))0x37ea190)(p,(int)value(b,0x64)+(bw-w)/2,(int)value(b,0x68)+5);}
static void rewardLines(unsigned i,const wchar_t*name,unsigned quantity){wchar_t full[160],first[160],key[40];AP_wsprintfW(full,L"%s ×%u",name,quantity);unsigned n=0;while(n<150&&full[n])n++;unsigned cut=n;AP_wsprintfW(key,L"FE_REWARD_%u",i);void*p=control(key);if(!readable(p,0x410)){failed=true;return;}
 for(;;){for(unsigned j=0;j<cut;j++)first[j]=full[j];first[cut]=0;label(key,first);int w=((int(__thiscall*)(void*))0x3835890)(p);if(w<=158||!cut)break;cut--;}
 indexedLabel(L"FE_REWARD2_",i,full+cut);
}
static void render(){if(!shown||!owns(true)||tab>=6)return;wchar_t s[160],key[40];const unsigned char*p=data+32+tab*256;unsigned count=ready?get32(p,4):0,tiers=ready?get32(p,24):0,n=tiers+(ready&&get32(p,8)?1:0);if(page*6>=n)page=0;
 const wchar_t*seasons[]={L"2016 · 春节赠礼",L"2016 · 五一赠礼",L"2016 · 夏日赠礼",L"2017 · 春节赠礼",L"2018 · 春节赠礼",L"2018 · 夏日赠礼"};
 const wchar_t*titles[]={L"问鼎天下",L"梦幻贵族",L"盛晴狂欢",L"猎龙者 · 苍龙啸天",L"兽人守护神",L"热舞一夏"};
 label(L"FE_SEASON",seasons[tab],0xffb8c2d4);label(L"FE_TITLE",titles[tab],0xff95d9f4);deco(L"FE_BANNER",1+tab,432,92);
 if(ready)AP_wsprintfW(s,L"本账号已购买 %u 套",count);else AP_wsprintfW(s,L"本账号已购买 -- 套");label(L"FE_COUNT",s,0xff87c9ed);
 AP_wsprintfW(s,L"第 %u / %u 页",page+1,n?(n+5)/6:1);label(L"FE_PAGE",s,0xffb8c2d4);
 for(unsigned i=0;i<6;i++){unsigned j=page*6+i;AP_wsprintfW(key,L"FE_CARD_%u",i);show(control(key),j<n);if(j>=n){indexedLabel(L"FE_AT_",i,L"");indexedLabel(L"FE_REWARD_",i,L"");indexedLabel(L"FE_REWARD2_",i,L"");indexedLabel(L"FE_STATE_",i,L"");continue;}unsigned char repeat[8]{};bool repeating=j==tiers;put16(repeat,0,(unsigned short)get32(p,8));repeat[2]=count>=get32(p,8)?2:0;repeat[3]=(unsigned char)get32(p,16);put32(repeat,4,get32(p,12));const unsigned char*r=repeating?repeat:p+64+j*8;deco(key,7+r[2],210,70);AP_wsprintfW(s,repeating?L"第 %u 套起":L"累计第 %u 套",get16(r,0));indexedLabel(L"FE_AT_",i,s,0xffa4c9dd);rewardLines(i,rewardName(get32(r,4)),r[3]);indexedLabel(L"FE_STATE_",i,repeating?L"每套一份":r[2]==2?L"已发邮件":r[2]==1?L"待发邮件":L"未达成",r[2]==2?0xff8ac898:r[2]==1?0xff79c9e8:0xffa0a6ac);}
 if(!ready)label(L"FE_TAIL",L"购买进度自动更新。");else {unsigned tail=get32(p,8);if(tail)AP_wsprintfW(s,L"第%u套起：每套赠送%s ×%u",tail,rewardName(get32(p,12)),get32(p,16));else AP_wsprintfW(s,L"本期赠礼至第6套；后续购买不追加档位奖励。");label(L"FE_TAIL",s);}
 buttonText(L"FE_PREV_TEXT",L"上一页",L"FE_PREV");buttonText(L"FE_NEXT_TEXT",L"下一页",L"FE_NEXT");
}
static void query(){if(!ui()||pending||failed)return;last=AP_GetTickCount();void*net=((void*(__cdecl*)())0x374ea10)();if(!readable(net,0x2bd06c)||!*((unsigned char*)net+8)||value(net,0x2bcc34)||value(net,0x2bcc2c)){ready=false;render();return;}
 unsigned char b[16]{};memcpy(b,"FEQ1",4);put16(b,4,1);if(++sequence==0)++sequence;put32(b,8,sequence);if(((int(__thiscall*)(void*,unsigned))0x374f280)(net,0x7f02)!=1)return;
 for(unsigned i=0;i<16;i+=4)((int(__thiscall*)(void*,unsigned))0x374f3f0)(net,get32(b,i));pending=sequence;since=last;((int(__cdecl*)())0x3750ac0)();
}
extern "C" __declspec(dllexport) void __cdecl ReceiveFestival(){unsigned n=*(unsigned*)0x57f9ad4;void*body=*(void**)0x57f9ad0;if(n!=1568||!readable(body,1568))return;unsigned char b[1568];((void(__cdecl*)(void*,unsigned))0x37e8430)(b,1568);if(_InterlockedCompareExchange(&lock,1,0))return;memcpy(incoming,b,1568);received=true;_InterlockedExchange(&lock,0);}
static void apply(){unsigned char b[1568];if(_InterlockedCompareExchange(&lock,1,0))return;bool got=received;if(got){memcpy(b,incoming,1568);received=false;}_InterlockedExchange(&lock,0);if(!got||!pending||get32(b,24)!=pending)return;pending=0;ready=false;
 if(!equal(b,"FES1",4)||get16(b,4)!=1||get16(b,6)!=1568||get16(b,8)||get16(b,12)!=6||!get16(b,10)||!(get32(b,16)|get32(b,20))){render();return;}
 const unsigned ids[]={201601,201605,201607,201701,201801,201807};for(unsigned i=0;i<6;i++){auto p=b+32+i*256;if(get32(p,0)!=ids[i]||get32(p,24)>24||get16(p,62)!=0){render();return;}for(unsigned j=0;j<get32(p,24);j++){auto r=p+64+j*8;if(!get16(r,0)||r[2]>2||!r[3]||!get32(r,4)){render();return;}}}
 memcpy(data,b,1568);ready=true;render();
}
static void close(){if(owns(true))((void(__thiscall*)(void*,int,int,int))0x32b94a0)(mgr,id,-1,0);shown=false;}
static int __fastcall event(void*,void*,int sender,int kind,int,int){if(kind!=13||!ui()||failed||!owns(true))return 0;void*p=(void*)sender;if(p==(void*)value(win,0x50)){close();return 1;}if(p==control(L"FE_PREV")){if(page)page--;render();return 1;}if(p==control(L"FE_NEXT")){if(ready&&(page+1)*6<get32(data+32+tab*256,24)+(get32(data+32+tab*256,8)?1u:0u))page++;render();return 1;}return 0;}
static void* events[]={(void*)&event};
static bool factoryRequest(int n,void*m){return creating&&n==id&&m==mgr&&ui();}
static void* factory(int n,void*m){void*mem=((void*(__cdecl*)(unsigned))0x37f2170)(0x1dc);if(!mem)return nullptr;void*answer;const wchar_t*xml=path;
 __asm {sub esp,20h
 mov ecx,esp
 push xml
 mov eax,0045fea0h
 call eax
 push n
 push m
 mov ecx,mem
 mov eax,029bb9c0h
 call eax
 mov answer,eax}
 win=answer;return answer;}
static bool layoutRequest(const wchar_t*p){return creating&&ui()&&p&&wequal(p,path);}
static bool layout(void*doc){if(!readable(doc,0x80)||value(doc)!=0x4627bb4)return false;((void(__thiscall*)(void*,const char*,void*,int))0x3e43eb0)(doc,(const char*)kFestivalLayoutXmlBytes,nullptr,0);return !*((unsigned char*)doc+0x48)&&((void*(__thiscall*)(void*))0x3e3f700)(doc);}
static bool freeSlot(int n){if(!readable(mgr,0xc640)||!value(mgr,0xc5b4))return false;for(unsigned o:{0x41u,0x1879u}){auto h=(char*)mgr+(n*5+o)*4;if(value(h,4)!=value(h,8))return false;}return value((void*)0x3373f58,n*4)==0x3373f41;}
static bool fit(){if(!readable(win,0x1dc))return false;panel=(void*)value(win,0x30);void*props=(void*)value(win,4);void*title=(void*)value(win,0x48);void*x=(void*)value(win,0x50);void*t=(void*)value(win,8);if(!readable(panel,0x100)||!readable(props,0x4c)||!readable(title,0x140)||!readable(x,0x504)||!readable(t,0x60))return false;
 *((unsigned char*)t+0x44)=1;*((unsigned char*)t+0x45)=1;*(void***)((char*)win+0x1b0)=events;
 *(unsigned*)((char*)props+0x24)=38;*(unsigned*)((char*)props+0x28)=34;*(unsigned*)((char*)props+0x44)=12;*(unsigned*)((char*)props+0x48)=13;*((unsigned char*)props+0x3b)=1;((void(__thiscall*)(void*,int))0x335f0d0)(win,0);((void(__thiscall*)(void*,bool))0x3826f40)(title,true);show(title,true);show(x,true);void*acc=(void*)value(win,0x58);if(acc)show(acc,false);
 deco(L"FE_BACKGROUND",0,456,442);deco(L"FE_LINE",10,432,1);for(auto key:{L"FE_PREV",L"FE_NEXT"})compact(control(key),0);
 if(!placed){((void(__thiscall*)(void*,int,int,int,int))0x335e970)(win,1,1,0,7);placed=true;}return true;}
static void open(){if(!ui()||failed||!contracts())return;if(win&&!owns(true)&&!owns(false)){win=panel=nullptr;shown=placed=false;}mgr=*(void**)0x56a3798;if(!win){bool found=false;for(int n:{1068,1213,1214,1216,992,1065})if(freeSlot(n)){id=n;found=true;break;}if(!found)return;creating=true;void*r=((void*(__thiscall*)(void*,int,int,int))0x32bea50)(mgr,id,0,0);creating=false;if(!r||r!=win){failed=true;return;}}
 if(((void*(__thiscall*)(void*,int,int,int))0x32bea50)(mgr,id,0,0)!=win||!fit()){failed=true;return;}shown=true;render();query();}
// Only the six server-owned, parsed UI/Event routes use our private pages.
extern "C" __declspec(dllexport) int __cdecl CalendarClick(void*row){if(!readable(row,0x16c))return 0;unsigned event=value(row);if(event<60001||event>60006||value(row,0x140)!=0x138||value(row,0x164)||value(row,0x168))return 0;tab=event-60001;page=0;open();return shown&&owns(true)?id:0;}
// Use the same root/absolute-position APIs as native EventCalendar::OpenEvent.
// Placement is done once per explicit click, retaining ordinary title dragging.
static int clamp(int n,int lo,int hi){if(hi<lo)hi=lo;return n<lo?lo:n>hi?hi:n;}
static void dock(void*calendar){
 if(!shown||!owns(true)||!readable(calendar,4)||!readable(win,4))return;
 Ref a{},b{};auto av=*(void***)calendar;auto bv=*(void***)win;
 ((Ref*(__thiscall*)(void*,Ref*))av[0x80/4])(calendar,&a);
 ((Ref*(__thiscall*)(void*,Ref*))bv[0x80/4])(win,&b);
 if(readable(a.value,0x84)&&readable(b.value,0x84)){
  int xy[2]{};((void*(__thiscall*)(void*,int*))0x37eb710)(a.value,xy);
  int aw=((int(__thiscall*)(void*))0x1ad59d0)(a.value),bw=((int(__thiscall*)(void*))0x1ad59d0)(b.value);
  int ah=((int(__thiscall*)(void*))0x37e9f60)(a.value),bh=((int(__thiscall*)(void*))0x37e9f60)(b.value);
  int sw=*(int*)0x55b0318,sh=*(int*)0x55b031c;
  if(aw>0&&bw>0&&ah>0&&bh>0&&aw<4096&&bw<4096&&sw>=400&&sh>=300){
   // TileType=0 roots exclude the 12px side borders and 25px title.
   // Leave 2px between OUTER frames, not between the content roots.
   const int left=12,right=12,top=25,bottom=11,gap=26;
   xy[0]=clamp(xy[0],left,sw-aw-right);
   int y=clamp(xy[1],top,sh-(ah>bh?ah:bh)-bottom),x=xy[0]+aw+gap;
   if(x+bw+right>sw){if(xy[0]-bw-gap>=left)x=xy[0]-bw-gap;
    else if(aw+bw+gap+left+right<=sw){xy[0]=clamp(xy[0],left,sw-aw-bw-gap-right);x=xy[0]+aw+gap;}
    else {xy[0]=left;x=sw-bw-right;}}
   x=clamp(x,left,sw-bw-right);xy[0]=clamp(xy[0],left,sw-aw-right);
   ((void(__thiscall*)(void*,int,int))0x37ecc40)(a.value,xy[0],y);
   ((void(__thiscall*)(void*,int,int))0x37ecc40)(b.value,x,y);
  }
 }
 ((void(__thiscall*)(Ref*))0xf11f90)(&b);((void(__thiscall*)(Ref*))0xf11f90)(&a);
}
extern "C" __declspec(dllexport) int __cdecl CalendarFromCalendar(void*row,void*calendar,unsigned caller){
 if(!readable(row,0x16c)||value(row)<60001||value(row)>60006||value(row,0x140)!=312||value(row,0x164)||value(row,0x168))return 0;
 // Rebuilding the calendar at 017c441e automatically opens the first row.
 // Return the native 'no popup' sentinel only for that exact automatic call.
 if(caller==0x17c4423)return 0x4d8;
 int opened=CalendarClick(row);if(opened)dock(calendar);return opened;
}
static void tick(){if(failed)return;if(shown&&!owns(true)){if(owns(false))shown=false;else{win=panel=nullptr;shown=placed=false;}}apply();unsigned now=AP_GetTickCount();if(pending&&now-since>8000){pending=0;ready=false;last=now;render();}if(!pending&&now-last>(shown?5000u:30000u))query();}
}
