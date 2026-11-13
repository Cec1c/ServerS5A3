#pragma once
#include "S5A3Bridge.h"
static void(__cdecl*dpsFeed)(const S5DpsFrame*);
static void(__cdecl*powerFeed)(const S5PowerFrame*);
static void(__cdecl*powerCompute)(combat_power::CombatSnapshot*,unsigned,unsigned*,wchar_t*);
static void(__cdecl*dpsToggle)();
static void loadPlugins(){
 static bool attempted;if(attempted)return;attempted=true;
 auto filename=(decltype(&GetModuleFileNameW))Proc(Module(L"kernel32.dll"),"GetModuleFileNameW");
 auto library=(decltype(&LoadLibraryExW))Proc(Module(L"kernel32.dll"),"LoadLibraryExW");
 if(!filename||!library)return;wchar_t path[520];unsigned n=filename(nullptr,path,490),base=n;
 if(!n||n>=490)return;while(base&&path[base-1]!=L'\\'&&path[base-1]!=L'/')base--;if(!base)return;
 const wchar_t* names[2]={L"DpsMeter.dll",L"CombatPower.dll"};
 for(unsigned i=0;i<2;i++){
  unsigned j=0;do{path[base+j]=names[i][j];}while(names[i][j++]);
  HMODULE m=library(path,nullptr,LOAD_WITH_ALTERED_SEARCH_PATH);if(!m)continue;
  auto init=(BOOL(__cdecl*)())Proc(m,"ClientPatchPluginInit");if(!init||!init())continue;
  if(!i){dpsFeed=(decltype(dpsFeed))Proc(m,"S5A3DpsFeed");dpsToggle=(decltype(dpsToggle))Proc(m,"S5A3DpsToggle");}
  else {powerCompute=(decltype(powerCompute))Proc(m,"S5A3PowerCompute");powerFeed=(decltype(powerFeed))Proc(m,"S5A3PowerFeed");}
 }
}
