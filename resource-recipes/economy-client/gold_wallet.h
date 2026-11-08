#pragma once
// Native wallet notification 53 already stores Token + HappyToken at 057b7240.
// Read through its original protected-value accessor; do not count Happy twice.
// The server remains authoritative and atomically debits Happy -> Token -> Cera.
extern "C" __declspec(dllexport) int __cdecl GoldWalletAvailable(int cera){
 unsigned tokens=0;
 ((void(__thiscall*)(void*,const void*,void*))0x37df550)((void*)0x57b7240,(void*)0x57b7244,&tokens);
 unsigned cash=cera>0?(unsigned)cera:0;
 if(tokens>0x7fffffffu-cash)return 0x7fffffff;
 return (int)(tokens+cash);
}
extern "C" __declspec(dllexport) __declspec(naked) void GoldWalletGate(){__asm{
 pushfd
 pushad
 push dword ptr [ebp-04b0h]
 call GoldWalletAvailable
 add esp,4
 mov dword ptr [ebp-04b0h],eax
 popad
 popfd
 mov eax,dword ptr [ebp+8]
 mov ecx,dword ptr [ebp-04b0h]
 push 02331d70h
 ret
}}

