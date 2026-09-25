package com.xgh.app.ui

import android.content.Intent
import android.os.Bundle
import android.view.View
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import com.xgh.app.XghApp
import com.xgh.app.data.ApiClient
import com.xgh.app.data.LoginResponse
import com.xgh.app.databinding.ActivityLoginBinding
import com.xgh.app.util.Ui
import kotlinx.coroutines.launch

/**
 * 宿管三要素免密登录：手机号 + 楼栋 + 姓名。
 * 楼栋要求完整输入（后端是双向子串匹配，只输数字会匹配错误楼栋）。
 */
class LoginActivity : AppCompatActivity() {

    private lateinit var binding: ActivityLoginBinding

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val app = application as XghApp

        // 已有 token：先重建网络层再尝试直进首页（打开 App 免密直登）
        val saved = app.appScope
        lifecycleScope.launch {
            val baseUrl = app.sessionStore.currentBaseUrl()
            ApiClient.get().rebuild(baseUrl)
            val token = app.sessionStore.currentToken()
            if (!token.isNullOrBlank()) tryAutoEnter()
        }

        binding = ActivityLoginBinding.inflate(layoutInflater)
        setContentView(binding.root)

        lifecycleScope.launch {
            binding.etServer.setText(app.sessionStore.currentBaseUrl())
        }

        binding.btnLogin.setOnClickListener { doLogin() }
    }

    private fun tryAutoEnter() {
        lifecycleScope.launch {
            val profile = Ui.request(this@LoginActivity) { ApiClient.get().service.profile() }
            if (profile != null && profile.id != 0L) {
                goHome()
            }
        }
    }

    private fun doLogin() {
        val server = binding.etServer.text?.toString()?.trim().orEmpty()
        val phone = binding.etPhone.text?.toString()?.trim().orEmpty()
        val building = binding.etBuilding.text?.toString()?.trim().orEmpty()
        val name = binding.etName.text?.toString()?.trim().orEmpty()

        if (phone.isEmpty() || building.isEmpty() || name.isEmpty()) {
            Ui.toast(this, "请填写手机号、楼栋与姓名")
            return
        }
        if (building.length < 2) {
            Ui.toast(this, "请输入完整楼栋名称，如：12号楼")
            return
        }

        binding.progress.visibility = View.VISIBLE
        binding.btnLogin.isEnabled = false

        val app = application as XghApp
        lifecycleScope.launch {
            try {
                val baseUrl = app.sessionStore.normalizeBaseUrl(server)
                ApiClient.get().rebuild(baseUrl)
                val resp = ApiClient.get().service.dormQuickLogin(
                    mapOf(
                        "phone" to phone,
                        "building" to building,
                        "real_name" to name
                    )
                ) as LoginResponse
                app.sessionStore.saveSession(resp.token, baseUrl, resp.user)
                ApiClient.get().currentToken = resp.token
                goHome()
            } catch (e: retrofit2.HttpException) {
                val body = try { e.response()?.errorBody()?.string() } catch (_: Exception) { null }
                val msg = Regex("\"error\"\\s*:\\s*\"([^\"]+)\"").find(body ?: "")?.groupValues?.get(1)
                Ui.toast(this@LoginActivity, msg ?: getString(com.xgh.app.R.string.net_error))
            } catch (e: Exception) {
                Ui.toast(this@LoginActivity, getString(com.xgh.app.R.string.net_error))
            } finally {
                binding.progress.visibility = View.GONE
                binding.btnLogin.isEnabled = true
            }
        }
    }

    private fun goHome() {
        startActivity(Intent(this, HomeActivity::class.java))
        finish()
    }
}
