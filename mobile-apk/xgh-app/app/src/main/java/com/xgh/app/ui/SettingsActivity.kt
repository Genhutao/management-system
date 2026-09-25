package com.xgh.app.ui

import android.content.Intent
import android.os.Bundle
import android.widget.Toast
import androidx.appcompat.app.AlertDialog
import androidx.appcompat.app.AppCompatActivity
import androidx.lifecycle.lifecycleScope
import com.xgh.app.R
import com.xgh.app.XghApp
import com.xgh.app.data.ApiClient
import com.xgh.app.data.User
import com.xgh.app.databinding.ActivitySettingsBinding
import com.xgh.app.util.Ui
import kotlinx.coroutines.launch

class SettingsActivity : AppCompatActivity() {

    private lateinit var binding: ActivitySettingsBinding

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        binding = ActivitySettingsBinding.inflate(layoutInflater)
        setContentView(binding.root)

        val app = application as XghApp
        lifecycleScope.launch {
            binding.etServer.setText(app.sessionStore.currentBaseUrl())
            // profile 实时校准本地展示（读库数据）
            val profile = Ui.request(this@SettingsActivity) { ApiClient.get().service.profile() }
            if (profile != null && profile.id != 0L) renderUser(profile)
        }

        binding.btnSaveServer.setOnClickListener {
            val url = binding.etServer.text?.toString()?.trim().orEmpty()
            if (url.isEmpty()) {
                Ui.toast(this, "请填写服务器地址")
                return@setOnClickListener
            }
            lifecycleScope.launch {
                app.sessionStore.saveBaseUrl(url)
                ApiClient.get().rebuild(app.sessionStore.currentBaseUrl())
                Ui.toast(this@SettingsActivity, "已保存")
            }
        }

        binding.btnLogout.setOnClickListener {
            AlertDialog.Builder(this)
                .setMessage(R.string.logout_confirm)
                .setPositiveButton(R.string.logout) { _, _ -> doLogout() }
                .setNegativeButton(android.R.string.cancel, null)
                .show()
        }
    }

    private fun renderUser(u: User) {
        binding.tvUser.text = buildString {
            append("姓名：${u.real_name ?: "-"}\n")
            append("楼栋：${u.building ?: "-"}\n")
            append("角色：${u.role ?: "-"}\n")
            u.department?.let { append("部门：$it\n") }
            u.position?.let { append("职务：$it") }
        }
    }

    private fun doLogout() {
        val app = application as XghApp
        lifecycleScope.launch {
            app.sessionStore.clear()
            ApiClient.get().currentToken = null
            Toast.makeText(this@SettingsActivity, "已退出", Toast.LENGTH_SHORT).show()
            startActivity(Intent(this@SettingsActivity, LoginActivity::class.java))
            finishAffinity()
        }
    }
}
