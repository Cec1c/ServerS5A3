#pragma once
static bool powerDismissed;
static unsigned powerPage,powerTarget,powerPaintAt;
static void*powerParent;
static int powerLastX,powerLastY,powerOffsetX,powerOffsetY,powerSettle;
static unsigned powerBonus;
static wchar_t powerRank[16];
static combat_power::CombatSnapshot powerState;

static void powerPercent(wchar_t*out,const wchar_t*name,unsigned hundredths){Format(out,L"%s %u.%02u%%",name,hundredths/100,hundredths%100);}
static void paintPower(){
 Panel&p=panels[3];if(!p.shown||!owned(p,true))return;
 const auto&c=powerState;wchar_t b[128];
 unsigned rank=0;const unsigned limits[]={3100,8100,23000,52000,65000,130000};
 while(rank<6&&c.totalScore>=limits[rank])rank++;
 for(unsigned i=0;i<7;i++){Format(b,L"CP_BADGE_%u",i);visible(control(p,b),c.baseValid&&rank==i);}
 text(control(p,L"CP_RANK"),c.baseValid?powerRank:L"属性读取中");
 if(c.baseValid)Format(b,L"%u",c.totalScore);else Format(b,L"读取中");text(control(p,L"CP_SCORE"),b);
 Format(b,L"%s Lv.%u",c.profession,c.level);text(control(p,L"CP_ROLE"),b);
 if(c.baseValid)Format(b,L"%u",c.baseScore);else Format(b,L"--");text(control(p,L"CP_BASE"),b);
 const wchar_t*labels[2][4]={{L"白字",L"黄字",L"装备",L"爆伤"},{L"黄追",L"爆追",L"装备",L"全攻"}};
 unsigned values[2][4]={{c.whiteDamageTenths*10,(c.yellowDamageTenths+c.yellowAdditionalTenths)*10,powerBonus,(c.criticalDamageTenths+c.criticalAdditionalTenths)*10},{c.yellowAdditionalTenths*10,c.criticalAdditionalTenths*10,powerBonus,c.allAttackTenths*10}};
 for(unsigned i=0;i<4;i++){wchar_t id[24];Format(id,L"CP_AFFIX_%u",i);powerPercent(b,labels[powerPage][i],values[powerPage][i]);text(control(p,id),b);}
 if(panels[4].shown&&owned(panels[4],true)){Format(b,L"当前：%u（%s） 装备：%u.%02u%%",c.totalScore,powerRank,powerBonus/100,powerBonus%100);text(control(panels[4],L"CP_GUIDE_SCORE"),b);}
}
void powerEvent(Panel&p,void*src){
 if(&p!=&panels[3])return;
 if(src==(void*)value(p.win,0x50)){powerDismissed=true;closePanel(p);for(unsigned i=4;i<6;i++)closePanel(panels[i]);return;}
 if(src==control(p,L"CP_PREV")||src==control(p,L"CP_NEXT")){powerPage^=1;paintPower();}
 for(unsigned i=4;i<6;i++)if(src==control(p,i==4?L"CP_GUIDE":L"CP_HELP")){if(panels[i].shown)closePanel(panels[i]);else openPanel(panels[i]);paintPower();}
}
static void nativePowerFrame(S5PowerFrame&f,void*parent){
 Panel&p=panels[3];
 if(!f.visible||!parent){
  for(unsigned i=3;i<6;i++)if(panels[i].shown)closePanel(panels[i]);
  powerParent=nullptr;powerTarget=0;powerDismissed=false;return;
 }
 bool first=powerParent!=parent||powerTarget!=f.stats.characterId;
 if(first){powerParent=parent;powerTarget=f.stats.characterId;powerDismissed=false;powerOffsetX=powerOffsetY=0;powerSettle=2;powerPage=0;}
 if(powerDismissed)return;
 if(!p.shown){if(!openPanel(p))return;powerSettle=2;first=true;}
 if(!owned(p,true)||!readable(p.root,0x84))return;
 // Read the same cached screen coordinates used by the native renderer
 // (209BBE0/37E9F40), not the alignment-relative position in 37EB710.
 int x=(int)value(p.root,0x7c),y=(int)value(p.root,0x80);
 if(!first&&!powerSettle&&(x!=powerLastX||y!=powerLastY)){
  powerOffsetX+=x-powerLastX;powerOffsetY+=y-powerLastY;
 }
 // CharacterInfo type 5: title origin -21 + sprite's 8px top offset.
 // This native type-0 title: -25 + 3px. Nine pixels aligns visible tops.
 int wantX=f.x+f.width+powerOffsetX,wantY=f.y+9+powerOffsetY;
 wantX=clamp(wantX,0,f.uiWidth-p.width-8);wantY=clamp(wantY,24,f.uiHeight-p.height-8);
 if(first||wantX!=x||wantY!=y){((void(__thiscall*)(void*,int,int))0x37ecc40)(p.root,wantX,wantY);powerSettle=2;}
 else if(powerSettle)powerSettle--;
 powerLastX=wantX;powerLastY=wantY;
 fitPowerChrome(p);
 powerState=f.stats;powerCompute(&powerState,f.equipmentV2,&powerBonus,powerRank);
 if(first||Time()-powerPaintAt>=100){powerPaintAt=Time();paintPower();}
}
