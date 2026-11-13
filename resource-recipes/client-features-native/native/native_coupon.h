#pragma once
#include "coupon_routes.h"
// Confirmation 02161F00 previously retained its dialog across the shop's
// window transition, then closed it afterwards. Copy the group, close the
// dialog first and never touch that object again. Keep the native shop,
// product availability and category navigation paths.
extern "C" __declspec(dllexport) bool __fastcall CouponConfirm(void*dialog,void*){
 if(!readable(dialog,0x1c4)||value(dialog)!=0x497406c)return false;
 unsigned group=(unsigned)value(dialog,0x1c0);
 void*manager=*(void**)0x56a3798;
 if(!readable(manager,0xc3c0))return false;
 void*shop=(void*)value(manager,0xc3bc);
 if(!readable(shop,0x10))return false;
 uintptr_t closer=value((void*)value(dialog),0x7c);
 ((void(__thiscall*)(void*))closer)(dialog);
 // Closing a window may trigger callbacks. Revalidate the owner before
 // invoking the already-created native cash shop.
 if(*(void**)0x56a3798!=manager||value(manager,0xc3bc)!=(uintptr_t)shop)return true;
 ((void(__thiscall*)(void*,int))0x1977820)(shop,1);
 if(*(void**)0x56a3798!=manager||value(manager,0xc3bc)!=(uintptr_t)shop)return true;
 for(const auto&r:couponRoutes){
  if(r.group!=group)continue;
  int index=0;void*product=((void*(__thiscall*)(void*,int,unsigned,int*))0x194d380)(shop,-1,r.base,&index);
  if(!readable(product,0xd8)||!((bool(__thiscall*)(void*,void*))0x195b570)(shop,product))continue;
  unsigned category=((unsigned(__thiscall*)(void*,unsigned))0x194b230)(shop,*(Byte*)product);
  if(*(Byte*)product&&((bool(__thiscall*)(void*,unsigned,unsigned,int,int))0x1978da0)(shop,category,(unsigned)value(product,0x14),0,1))return true;
 }
 // A coupon for an off-sale period opens the ordinary shop. It must never
 // fall through an obsolete product pointer, spend currency or consume it.
 ((void(__thiscall*)(void*,int,int))0x1974850)(shop,0,0);
 return true;
}
