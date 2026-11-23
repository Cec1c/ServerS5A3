#define ATLAS_TEST
#include "AtlasWorkers101.cpp"
#include <vector>
#include <thread>
#include <array>
#include <cstdio>
#include <cassert>

static std::vector<BYTE> encoded(const Entry& item) {
    wchar_t path[MAX_PATH];
    assert(SUCCEEDED(StringCchPrintfW(path,_countof(path),L"%s%s",testFolder,item.pack)));
    FILE* f=nullptr;
    assert(!_wfopen_s(&f,path,L"rb") && !fseek(f,item.offset,SEEK_SET));
    std::vector<BYTE> buf(item.encoded);
    assert(fread(buf.data(),1,buf.size(),f)==buf.size());
    fclose(f);
    return buf;
}
static void check(const Entry& item,bool invalid) {
    auto src=encoded(item);
    std::vector<BYTE> actual(item.raw+1,0xcd),expected(item.raw);
    uLongf size=item.raw,ref=item.raw;
    assert(uncompress(expected.data(),&ref,src.data(),item.encoded)==Z_OK);
    assert(TryInflateAtlas(actual.data(),&size,src.data(),item.encoded)==Z_OK);
    assert(size==ref && actual[item.raw]==0xcd && !memcmp(actual.data(),expected.data(),size));
    if (invalid) {
        size=item.raw-1;
        assert(TryInflateAtlas(actual.data(),&size,src.data(),item.encoded)==Z_BUF_ERROR);
        src[0]=0;size=item.raw;
        assert(TryInflateAtlas(actual.data(),&size,src.data(),item.encoded)==Z_DATA_ERROR);
    }
}
static std::vector<BYTE> storedStream(const std::vector<BYTE>& plain) {
    unsigned n=static_cast<unsigned>(plain.size());
    assert(n<=65535);
    std::vector<BYTE> stream={0x78,1,1,static_cast<BYTE>(n),static_cast<BYTE>(n>>8),
        static_cast<BYTE>(~n),static_cast<BYTE>((~n)>>8)};
    stream.insert(stream.end(),plain.begin(),plain.end());
    DWORD sum=adler32(1,plain.data(),n);
    for(int shift:{24,16,8,0}) stream.push_back(static_cast<BYTE>(sum>>shift));
    return stream;
}
static void verifyRepeated() {
    std::vector<BYTE> plain(32768,0x53),out(plain.size()+1,0xbc);
    auto blob=storedStream(plain);
    auto call=[&](){
        uLongf size=static_cast<uLongf>(plain.size());
        assert(TryInflateAtlas(out.data(),&size,blob.data(),static_cast<DWORD>(blob.size()))==Z_OK &&
               size==plain.size() && !memcmp(out.data(),plain.data(),size) && out.back()==0xbc);
    };
    call();assert(!stats->repeatEntries && !stats->repeatHits);
    call();assert(stats->repeatEntries==1 && !stats->repeatHits);
    call();assert(stats->repeatHits==1);
    uLongf size=64;
    assert(TryInflateAtlas(out.data(),&size,blob.data(),static_cast<DWORD>(blob.size()))==Z_BUF_ERROR);
    blob[0]=0;size=static_cast<uLongf>(plain.size());
    assert(TryInflateAtlas(out.data(),&size,blob.data(),static_cast<DWORD>(blob.size()))==Z_DATA_ERROR && stats->repeatHits==1);
    for (unsigned i=0;i<500;++i) {
        memcpy(plain.data(),&i,sizeof(i));blob=storedStream(plain);
        call();call();call();
    }
    assert(stats->repeatBytes<=repeatBudget && stats->repeatBytes>repeatBudget-2*static_cast<LONG>(plain.size())-100);
}
int wmain(int argc,wchar_t** argv) {
    assert(argc==4);
    testFolder=argv[1];testWorkerLimit=static_cast<unsigned>(_wtoi(argv[2]));
    assert(testWorkerLimit>=1 && testWorkerLimit<=6);
    LARGE_INTEGER f,a,b;QueryPerformanceFrequency(&f);QueryPerformanceCounter(&a);
    InitOnceExecuteOnce(&once,initialize,nullptr,nullptr);
    assert(WaitForSingleObject(testComplete,10000)==WAIT_OBJECT_0);
    QueryPerformanceCounter(&b);
    if (!wcscmp(argv[3],L"missing")) {
        assert(stats->failed==_countof(entries) && !stats->bytesReady);
        puts("Missing archives: every job fell back.");return 0;
    }
    assert(!stats->failed && stats->finished==static_cast<LONG>(testWorkerLimit));
    if (!wcscmp(argv[3],L"bench")) {
        printf("%u,%.5f,%ld,%ld\n",testWorkerLimit,1000.*(b.QuadPart-a.QuadPart)/f.QuadPart,stats->peak,stats->bytesReady);
        return 0;
    }
    verifyRepeated();
    unsigned bytes=0;
    for(const Entry& item:entries){check(item,true);bytes+=item.encoded+item.raw;}
    assert(stats->hit==_countof(entries) && stats->bytesReady==static_cast<LONG>(bytes));
    for(unsigned i=0;i<testWorkerLimit;++i)assert((i<2?stats->jobs[i]:stats->extraJobs[i-2])>0);
    printf("exact=%zu invalid=%zu workers=%u peak=%ld cacheBudget=%ld repeatEntries=%ld bytes=%u\n",
        _countof(entries),_countof(entries)*2,testWorkerLimit,stats->peak,stats->repeatBytes,stats->repeatEntries,bytes);
}
