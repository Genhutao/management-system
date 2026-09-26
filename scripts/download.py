import threading
import requests
import time

URL = "http://103.236.77.86:19198/uploads/064bf4ac_report_1790330779914.jpg"
THREADS = 32
REQUESTS_PER_THREAD = 10  # 每个线程发 10 次

results = []
lock = threading.Lock()

def worker(thread_id):
    for i in range(REQUESTS_PER_THREAD):
        try:
            start = time.time()
            resp = requests.get(URL, timeout=5)
            cost = (time.time() - start) * 1000  # 毫秒
            with lock:
                results.append((thread_id, resp.status_code, cost))
            print(f"[线程{thread_id}] 第{i+1}次 -> {resp.status_code} ({cost:.0f}ms)")
        except Exception as e:
            with lock:
                results.append((thread_id, "ERROR", 0))
            print(f"[线程{thread_id}] 第{i+1}次 -> 失败: {e}")

if __name__ == "__main__":
    threads = []
    start_time = time.time()
    for tid in range(THREADS):
        t = threading.Thread(target=worker, args=(tid,))
        threads.append(t)
        t.start()

    for t in threads:
        t.join()

    total_time = time.time() - start_time
    success = sum(1 for r in results if r[1] == 200)
    print(f"\n===== 汇总 =====")
    print(f"总请求数: {len(results)}")
    print(f"成功: {success}, 失败: {len(results) - success}")
    print(f"总耗时: {total_time:.2f}s")
    print(f"QPS: {len(results) / total_time:.2f}")
