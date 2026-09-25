package com.xgh.app.data

import okhttp3.MultipartBody
import okhttp3.OkHttpClient
import okhttp3.RequestBody
import okhttp3.ResponseBody
import retrofit2.Retrofit
import retrofit2.converter.gson.GsonConverterFactory
import retrofit2.http.Body
import retrofit2.http.GET
import retrofit2.http.Multipart
import retrofit2.http.POST
import retrofit2.http.Part
import retrofit2.http.Query
import java.util.concurrent.TimeUnit

interface XghApi {

    @POST("/api/v1/auth/dorm-quick-login")
    suspend fun dormQuickLogin(@Body body: Map<String, String>): LoginResponse

    @GET("/api/v1/auth/profile")
    suspend fun profile(): User

    @GET("/api/v1/dorm/today-tasks")
    suspend fun todayTasks(): TodayTasksResponse

    @GET("/api/v1/dorm/slot-notice")
    suspend fun slotNotice(): SlotNoticeResponse

    @Multipart
    @POST("/api/v1/dorm/upload-photo")
    suspend fun uploadPhoto(
        @Part("room_number") roomNumber: RequestBody,
        @Part("photo_type") photoType: RequestBody,
        @Part("building") building: RequestBody,
        @Part("report_kind") reportKind: RequestBody,
        @Part("note_text") noteText: RequestBody,
        @Part("subject_names") subjectNames: RequestBody,
        @Part image: MultipartBody.Part?
    ): UploadPhotoResponse

    @GET("/api/v1/dorm/inspections")
    suspend fun inspections(
        @Query("category") category: String?,
        @Query("severity") severity: String?
    ): InspectionsResponse
}

/**
 * 服务器地址 App 内可配置：改地址时调用 [rebuild] 重建 Retrofit。
 * 401 统一回调给 Activity 跳回登录页。
 */
class ApiClient private constructor() {

    var onUnauthorized: (() -> Unit)? = null

    @Volatile
    private var api: XghApi = build(DEFAULT_BASE_URL_PLACEHOLDER)

    val service: XghApi get() = api

    fun rebuild(baseUrl: String) {
        api = build(baseUrl)
    }

    private fun build(baseUrl: String): XghApi {
        val client = OkHttpClient.Builder()
            .connectTimeout(15, TimeUnit.SECONDS)
            .readTimeout(60, TimeUnit.SECONDS)   // 图片上传可能较慢
            .writeTimeout(60, TimeUnit.SECONDS)
            .addInterceptor { chain ->
                val req = chain.request()
                val token = currentToken?.takeIf { it.isNotBlank() }
                chain.proceed(
                    if (token != null) req.newBuilder().header("Authorization", "Bearer $token").build() else req
                )
            }
            .addInterceptor { chain ->
                val resp = chain.proceed(chain.request())
                if (resp.code == 401) onUnauthorized?.postToMain()
                resp
            }
            .build()
        return Retrofit.Builder()
            .baseUrl(ensureSlash(baseUrl))
            .client(client)
            .addConverterFactory(GsonConverterFactory.create())
            .build()
            .create(XghApi::class.java)
    }

    private fun ensureSlash(url: String) = if (url.endsWith("/")) url else "$url/"

    /** 登录前还没有 token，用临时变量跨线程传递 */
    @Volatile
    var currentToken: String? = null

    private fun (() -> Unit).postToMain() {
        android.os.Handler(android.os.Looper.getMainLooper()).post(this)
    }

    companion object {
        private const val DEFAULT_BASE_URL_PLACEHOLDER = "http://192.168.1.100:8080/"

        @Volatile
        private var instance: ApiClient? = null

        fun get(): ApiClient = instance ?: synchronized(this) {
            instance ?: ApiClient().also { instance = it }
        }
    }
}
