package com.xgh.app.util

import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import com.xgh.app.R
import retrofit2.HttpException
import java.io.IOException

/**
 * 统一请求封装：后端 "HTTP 200 ≠ 成功" 是普遍现象，网络层只能解析 error 字段；
 * 401 已由拦截器统一回调，这里转成网络异常提示。
 */
object Ui {

    fun toast(context: AppCompatActivity, msg: String?) {
        Toast.makeText(context, msg ?: "未知错误", Toast.LENGTH_SHORT).show()
    }

    /** 从 \{"error": "…"\} 响应体里取中文说明 */
    fun extractError(body: String?): String? =
        Regex("\"error\"\\s*:\\s*\"([^\"]+)\"").find(body ?: "")?.groupValues?.get(1)

    suspend fun <T> request(
        activity: AppCompatActivity,
        call: suspend () -> T
    ): T? = try {
        call()
    } catch (e: HttpException) {
        val body = try { e.response()?.errorBody()?.string() } catch (_: Exception) { null }
        val msg = extractError(body)
        toast(activity, msg ?: activity.getString(R.string.net_error))
        null
    } catch (e: IOException) {
        toast(activity, activity.getString(R.string.net_error))
        null
    } catch (e: Exception) {
        toast(activity, e.message)
        null
    }
}
